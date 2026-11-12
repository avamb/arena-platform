package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// Statuses of a conversion.
const (
	StatusOK          = "ok"
	StatusNeedsReview = "needs_review"
	// MaxUploadBodyBytes is the request body limit of the arena API (1 MiB).
	MaxUploadBodyBytes = 1 << 20
	StatusFailed       = "failed"
	StatusSkipped      = "skipped"
)

var sbtSectRE = regexp.MustCompile(`\ssbt:sect\s*=`)

const (
	positionTolerance = 0.01
	rowLabelPitches   = 1.6
	maxListed         = 5
)

// RowCount is the number of seats in one (sector, row).
type RowCount struct {
	Sector string `json:"sector"`
	Row    string `json:"row"`
	Seats  int    `json:"seats"`
}

// Report is the content of convert.json.
type Report struct {
	Status            string     `json:"status"`
	Dialect           string     `json:"dialect"`
	Reasons           []string   `json:"reasons"`
	SourceSeats       int        `json:"source_seats"`
	HouseSeats        int        `json:"house_seats"`
	SchemaSeats       int        `json:"schema_seats"`
	SourceRows        []RowCount `json:"source_rows"`
	SchemaRows        []RowCount `json:"schema_rows"`
	HouseRows         []RowCount `json:"house_rows"`
	Schema            string     `json:"schema"`
	Categories        int        `json:"categories"`
	StageFound        bool       `json:"stage_found"`
	StageLabel        string     `json:"stage_label"`
	Scale             float64    `json:"scale"`
	RemovedRowNumbers int        `json:"removed_row_numbers"`
	Warnings          []string   `json:"warnings"`
	Errors            []string   `json:"errors"`
	MergedNames       []string   `json:"merged_names"`

	Adapted         []string       `json:"adapted"`
	SeatsByCategory map[string]int `json:"seats_by_category"`
	Sectors         int            `json:"sectors"`
	Rows            int            `json:"rows"`
}

var stageRE = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(сцена|stage|scéna|jeviště|podium|bühne|scena|escenario|screen|экран|ekran|plátno|platno|pantalla|scène|scene|leinwand|bina|lava)(?:[^\p{L}]|$)`)
var bareNumberRE = regexp.MustCompile(`^\d+$`)
var viewBoxRE = regexp.MustCompile(`^\s*([-+0-9.eE]+)[\s,]+([-+0-9.eE]+)[\s,]+[-+0-9.eE]+[\s,]+[-+0-9.eE]+\s*$`)

// Restyle converts one sbt plan (and optional GET_SCHEMA answer) into house
// style. The house SVG is nil when the source could not be imported. It never
// repairs anything: every mismatch lands in the report.
func Restyle(source, schemaRaw []byte) ([]byte, Report) {
	rep := Report{
		Reasons:     []string{},
		SourceRows:  []RowCount{},
		SchemaRows:  []RowCount{},
		HouseRows:   []RowCount{},
		Scale:       1,
		Warnings:    []string{},
		Errors:      []string{},
		MergedNames: []string{},
		Schema:      "absent",

		Adapted:         []string{},
		SeatsByCategory: map[string]int{},
	}

	var (
		g     seating.Geometry
		warns []seating.ValidationError
		errs  seating.ValidationErrors
	)
	raw := source // what the merge guard scans: never the adapted copy
	if sbtSectRE.Match(source) {
		rep.Dialect = "sbt"
		imported := source
		if adapted, ok := adaptTemplate(source); ok {
			rep.Dialect = "sbt-template"
			rep.Adapted = []string{"seat ids and states added for the importer", "categories named by ordinal"}
			imported = adapted
			source = adapted
		}
		var plan seating.SBTPlan
		plan, warns, errs = seating.ImportSBTSVG(imported)
		g = plan.Geometry
	} else {
		rep.Dialect = "authoring"
		g, warns, errs = seating.ImportSVG(source)
	}
	for _, w := range warns {
		rep.Warnings = append(rep.Warnings, "source: "+w.Error())
	}
	for _, c := range g.Categories {
		if c.IsGA() {
			rep.Status = StatusFailed
			rep.Reasons = append(rep.Reasons, "general-admission area: not converted")
			return nil, rep
		}
	}
	if len(errs) > 0 {
		// A merge usually shows up as duplicate seats: name it as such.
		if reason, names := mergeGuard(raw, rep.Dialect, g, true); reason != "" {
			rep.Status = StatusFailed
			rep.Reasons = append(rep.Reasons, reason)
			rep.MergedNames = names
			return nil, rep
		}
		for _, e := range errs {
			rep.Errors = append(rep.Errors, "source: "+e.Error())
		}
		rep.Status = StatusFailed
		rep.Reasons = append(rep.Reasons, "source import failed: "+errs[0].Error())
		return nil, rep
	}
	rep.SourceSeats = g.SeatCount()
	rep.SourceRows = rowCounts(g)
	rep.Sectors = len(g.Sections)
	rep.Rows = len(rep.SourceRows)
	{
		names := map[int]string{}
		for _, c := range g.Categories {
			names[c.Index] = c.Name
		}
		for _, sec := range g.Sections {
			for _, r := range sec.Rows {
				for _, s := range r.Seats {
					rep.SeatsByCategory[names[s.CategoryIndex]]++
				}
			}
		}
	}

	if reason, names := mergeGuard(raw, rep.Dialect, g, false); reason != "" {
		rep.Status = StatusFailed
		rep.Reasons = append(rep.Reasons, reason)
		rep.MergedNames = names
		return nil, rep
	}

	var failed, review []string

	if minX, minY, ok := viewBoxOrigin(source); !ok || minX != 0 || minY != 0 {
		review = append(review, "viewBox origin is not 0 0")
	}

	// Which categories are used by at least one seat.
	used := map[int]bool{}
	for _, sec := range g.Sections {
		for _, r := range sec.Rows {
			for _, s := range r.Seats {
				used[s.CategoryIndex] = true
			}
		}
	}
	catByIndex := map[int]seating.Category{}
	var usedCats []seating.Category
	for _, c := range g.Categories {
		catByIndex[c.Index] = c
		if used[c.Index] {
			usedCats = append(usedCats, c)
		}
	}
	rep.Categories = len(usedCats)

	// Scale.
	scale := 1.0
	if m := math.Max(g.Canvas.Width, g.Canvas.Height); m > seating.MaxCanvasDimension {
		scale = math.Floor(float64(seating.MaxCanvasDimension)/m*1e6) / 1e6
	}
	rep.Scale = scale

	// Decor.
	decor, removed, stage, err := prepareDecor(g.DecorSVG)
	if err != nil {
		rep.Status = StatusFailed
		rep.Errors = append(rep.Errors, "decor: "+err.Error())
		rep.Reasons = append(rep.Reasons, "decor could not be read: "+err.Error())
		return nil, rep
	}
	rep.RemovedRowNumbers = removed
	rep.StageFound = stage != ""
	rep.StageLabel = stage
	if !rep.StageFound {
		review = append(review, "stage label not found")
	}

	house, labelReview := buildHouse(g, usedCats, decor, scale)
	review = append(review, labelReview...)

	// Proof: read the house plan back.
	hg, hwarns, herrs := seating.ImportSVG(house)
	for _, w := range hwarns {
		rep.Warnings = append(rep.Warnings, "house: "+w.Error())
	}
	for _, e := range herrs {
		rep.Errors = append(rep.Errors, "house: "+e.Error())
	}
	if len(herrs) > 0 {
		failed = append(failed, "house.svg failed ImportSVG: "+herrs[0].Error())
	}
	for _, e := range seating.ValidateForPlanType(hg, "assigned_seats") {
		rep.Errors = append(rep.Errors, "house: "+e.Error())
		failed = append(failed, "house.svg failed ValidateForPlanType: "+e.Error())
	}
	rep.HouseSeats = hg.SeatCount()
	rep.HouseRows = rowCounts(hg)
	failed = append(failed, compareGeometry(g, hg, scale, catByIndex)...)

	// schema.json.
	if schemaRaw != nil {
		counts, rows, found := schemaCounts(schemaRaw)
		if !found {
			rep.Schema = "not understood"
		} else {
			rep.SchemaRows = rows
			total := 0
			for _, r := range rows {
				total += r.Seats
			}
			rep.SchemaSeats = total
			if sameCounts(counts, sourceCountsByName(g)) {
				rep.Schema = "ok"
			} else {
				rep.Schema = "mismatch"
				review = append(review, fmt.Sprintf(
					"seat counts differ from schema.json (source %d seats, schema %d)",
					rep.SourceSeats, total))
			}
		}
	}

	// The plan-version route takes the SVG inside a JSON body of at most
	// MaxUploadBodyBytes. A larger plan cannot be loaded, so it is not "ok".
	if body, err := json.Marshal(map[string]string{"svg": string(house)}); err == nil && len(body) > MaxUploadBodyBytes {
		review = append(review, fmt.Sprintf(
			"too large for the upload route: %d bytes, the limit is %d (an embedded picture in the source backdrop?)",
			len(body), MaxUploadBodyBytes))
	}

	rep.Reasons = append(rep.Reasons, failed...)
	rep.Reasons = append(rep.Reasons, review...)
	switch {
	case len(failed) > 0:
		rep.Status = StatusFailed
		return nil, rep
	case len(review) > 0:
		rep.Status = StatusNeedsReview
	default:
		rep.Status = StatusOK
	}
	return house, rep
}

// ConvertPlan converts the plan in dir: reads source.svg and schema.json,
// writes house.svg and convert.json.
func ConvertPlan(dir string) (Report, error) {
	if raw, err := os.ReadFile(filepath.Join(dir, "plan.json")); err == nil {
		var meta struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &meta); err != nil {
			return Report{}, fmt.Errorf("plan.json: %w", err)
		}
		if meta.Status != "downloaded" {
			return Report{Status: StatusSkipped}, nil
		}
	} else if !os.IsNotExist(err) {
		return Report{}, err
	}
	source, err := os.ReadFile(filepath.Join(dir, "source.svg"))
	if err != nil {
		return Report{}, err
	}
	schema, err := os.ReadFile(filepath.Join(dir, "schema.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			return Report{}, err
		}
		schema = nil
	}
	house, rep := Restyle(source, schema)
	housePath := filepath.Join(dir, "house.svg")
	if rep.Status == StatusFailed {
		house = nil
	}
	if house != nil {
		if err := os.WriteFile(housePath, house, 0o600); err != nil {
			return rep, err
		}
	} else if err := os.Remove(housePath); err != nil && !os.IsNotExist(err) {
		return rep, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		return rep, err
	}
	if err := os.WriteFile(filepath.Join(dir, "convert.json"), buf.Bytes(), 0o600); err != nil {
		return rep, err
	}
	return rep, nil
}

// FindPlans returns every folder under root that holds a source.svg, sorted.
func FindPlans(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "source.svg" {
			out = append(out, filepath.Dir(path))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// --- geometry helpers -----------------------------------------------------

func rowCounts(g seating.Geometry) []RowCount {
	out := []RowCount{}
	for _, sec := range g.Sections {
		for _, r := range sec.Rows {
			out = append(out, RowCount{Sector: sec.Name, Row: r.Name, Seats: len(r.Seats)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sector != out[j].Sector {
			return out[i].Sector < out[j].Sector
		}
		return out[i].Row < out[j].Row
	})
	return out
}

type seatInfo struct {
	x, y  float64
	name  string
	color string
}

func seatMap(g seating.Geometry, scale float64, cats map[int]seating.Category) map[string]seatInfo {
	m := map[string]seatInfo{}
	for _, sec := range g.Sections {
		for _, r := range sec.Rows {
			for _, s := range r.Seats {
				c := cats[s.CategoryIndex]
				m[s.Key] = seatInfo{x: s.X * scale, y: s.Y * scale, name: c.Name, color: c.Color}
			}
		}
	}
	return m
}

func rowKeyCounts(g seating.Geometry) map[string]int {
	m := map[string]int{}
	for _, sec := range g.Sections {
		for _, r := range sec.Rows {
			m[sec.Key+"|"+r.Key] += len(r.Seats)
		}
	}
	return m
}

func compareGeometry(src, house seating.Geometry, scale float64, srcCats map[int]seating.Category) []string {
	var out []string
	sc, hc := rowKeyCounts(src), rowKeyCounts(house)
	var bad []string
	keys := map[string]bool{}
	for k := range sc {
		keys[k] = true
	}
	for k := range hc {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		if sc[k] != hc[k] {
			bad = append(bad, fmt.Sprintf("%s: source %d, house %d", k, sc[k], hc[k]))
		}
	}
	if len(bad) > 0 {
		out = append(out, fmt.Sprintf("seat counts differ in %d rows (%s)", len(bad), listed(bad)))
	}

	houseCats := map[int]seating.Category{}
	for _, c := range house.Categories {
		houseCats[c.Index] = c
	}
	sm := seatMap(src, scale, srcCats)
	hm := seatMap(house, 1, houseCats)
	var missing, extra, moved, recat []string
	for k, s := range sm {
		h, ok := hm[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		if math.Abs(h.x-s.x) > positionTolerance || math.Abs(h.y-s.y) > positionTolerance {
			moved = append(moved, k)
		}
		if h.name != s.name || h.color != s.color {
			recat = append(recat, k)
		}
	}
	for k := range hm {
		if _, ok := sm[k]; !ok {
			extra = append(extra, k)
		}
	}
	for _, p := range []struct {
		list []string
		what string
	}{
		{missing, "seats missing in house.svg"},
		{extra, "seats added in house.svg"},
		{moved, "seat positions differ"},
		{recat, "seat categories differ"},
	} {
		if len(p.list) > 0 {
			sort.Strings(p.list)
			out = append(out, fmt.Sprintf("%s: %d (%s)", p.what, len(p.list), listed(p.list)))
		}
	}
	return out
}

func listed(items []string) string {
	if len(items) > maxListed {
		return strings.Join(items[:maxListed], "; ") + "; ..."
	}
	return strings.Join(items, "; ")
}

func viewBoxOrigin(source []byte) (minX, minY float64, ok bool) {
	dec := xml.NewDecoder(bytes.NewReader(source))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	for {
		tok, err := dec.Token()
		if err != nil {
			return 0, 0, false
		}
		se, isStart := tok.(xml.StartElement)
		if !isStart {
			continue
		}
		for _, a := range se.Attr {
			if a.Name.Local != "viewBox" {
				continue
			}
			m := viewBoxRE.FindStringSubmatch(a.Value)
			if m == nil {
				return 0, 0, false
			}
			x, e1 := strconv.ParseFloat(m[1], 64)
			y, e2 := strconv.ParseFloat(m[2], 64)
			if e1 != nil || e2 != nil {
				return 0, 0, false
			}
			return x, y, true
		}
		return 0, 0, false
	}
}

// --- schema.json ----------------------------------------------------------

var (
	schemaSectorKeys = []string{"sectorname", "sector", "sectortitle"}
	schemaRowKeys    = []string{"rowname", "row", "rownumber", "rownum"}
	schemaNumberKeys = []string{"seatnumber", "seatnum", "number", "seat", "place", "placenumber", "num"}
	keyCleaner       = strings.NewReplacer("_", "", "-", "")
)

func nameKey(sector, row string) string {
	return strings.ToLower(strings.TrimSpace(sector)) + "|" + strings.ToLower(strings.TrimSpace(row))
}

func scalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, strings.TrimSpace(t) != ""
	case json.Number:
		return t.String(), true
	}
	return "", false
}

func pickScalar(lowered map[string]any, keys []string) (string, bool) {
	for _, k := range keys {
		if v, ok := lowered[k]; ok {
			if s, ok := scalarString(v); ok {
				return s, true
			}
		}
	}
	return "", false
}

// schemaCounts walks the GET_SCHEMA answer for seat objects (sector, row and
// number-like keys) and counts them per (sector, row).
func schemaCounts(raw []byte) (map[string]int, []RowCount, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, nil, false
	}
	counts := map[string]int{}
	names := map[string][2]string{}
	found := 0
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			lowered := make(map[string]any, len(t))
			for k, e := range t {
				lowered[keyCleaner.Replace(strings.ToLower(k))] = e
			}
			sector, ok1 := pickScalar(lowered, schemaSectorKeys)
			row, ok2 := pickScalar(lowered, schemaRowKeys)
			_, ok3 := pickScalar(lowered, schemaNumberKeys)
			if ok1 && ok2 && ok3 {
				k := nameKey(sector, row)
				counts[k]++
				names[k] = [2]string{sector, row}
				found++
				return
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		}
	}
	walk(root)
	if found == 0 {
		return nil, nil, false
	}
	rows := make([]RowCount, 0, len(counts))
	for k, n := range counts {
		rows = append(rows, RowCount{Sector: names[k][0], Row: names[k][1], Seats: n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Sector != rows[j].Sector {
			return rows[i].Sector < rows[j].Sector
		}
		return rows[i].Row < rows[j].Row
	})
	return counts, rows, true
}

func sourceCountsByName(g seating.Geometry) map[string]int {
	m := map[string]int{}
	for _, sec := range g.Sections {
		for _, r := range sec.Rows {
			m[nameKey(sec.Name, r.Name)] += len(r.Seats)
		}
	}
	return m
}

func sameCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// --- decor ----------------------------------------------------------------

type dNode struct {
	name  xml.Name
	attrs []xml.Attr
	kids  []dChild
}

type dChild struct {
	el   *dNode
	text string
}

var houseNamespaces = map[string]string{
	"http://www.inkscape.org/namespaces/inkscape":        "inkscape",
	"http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd": "sodipodi",
	"http://www.w3.org/2000/svg":                         "svg",
}

const wrapOpen = `<r xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" xmlns:sodipodi="http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd" xmlns:svg="http://www.w3.org/2000/svg">`

func parseFragment(fragment string) (*dNode, error) {
	dec := xml.NewDecoder(strings.NewReader(wrapOpen + fragment + "</r>"))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	var root *dNode
	var stack []*dNode
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &dNode{name: t.Name, attrs: append([]xml.Attr(nil), t.Attr...)}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, dChild{el: n})
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, dChild{text: string(t)})
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("no element found")
	}
	return root, nil
}

func (n *dNode) attr(local string) string {
	for _, a := range n.attrs {
		if a.Name.Local == local && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}

func (n *dNode) text() string {
	var b strings.Builder
	for _, k := range n.kids {
		if k.el != nil {
			b.WriteString(k.el.text())
		} else {
			b.WriteString(k.text)
		}
	}
	return b.String()
}

// joinedText is the text of n with the texts of its child elements (tspans)
// joined by a space.
func (n *dNode) joinedText() string {
	var parts []string
	for _, k := range n.kids {
		if k.el != nil {
			parts = append(parts, k.el.joinedText())
		} else {
			parts = append(parts, k.text)
		}
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func (n *dNode) descendants(local string, out *[]*dNode) {
	for _, k := range n.kids {
		if k.el == nil {
			continue
		}
		if k.el.name.Local == local {
			*out = append(*out, k.el)
		}
		k.el.descendants(local, out)
	}
}

func isNumberText(n *dNode) bool {
	if n.name.Local != "text" {
		return false
	}
	var spans []*dNode
	n.descendants("tspan", &spans)
	if len(spans) == 0 {
		return bareNumberRE.MatchString(strings.TrimSpace(n.text()))
	}
	for _, s := range spans {
		if !bareNumberRE.MatchString(strings.TrimSpace(s.text())) {
			return false
		}
	}
	return true
}

// prune drops the source legend, the price table and the row numbers.
func prune(n *dNode, removed *int) {
	kept := n.kids[:0]
	for _, k := range n.kids {
		if k.el != nil {
			id := k.el.attr("id")
			if id == "Legend" || id == "PriceCategory" {
				continue
			}
			if isNumberText(k.el) {
				*removed++
				continue
			}
			prune(k.el, removed)
		}
		kept = append(kept, k)
	}
	n.kids = kept
}

// prepareDecor returns the decor fragment without legend, price table and row
// numbers, the number of removed row-number texts, and the first stage label.
func prepareDecor(fragment string) (string, int, string, error) {
	if strings.TrimSpace(fragment) == "" {
		return "", 0, "", nil
	}
	root, err := parseFragment(fragment)
	if err != nil {
		return "", 0, "", err
	}
	removed := 0
	prune(root, &removed)

	var texts []*dNode
	root.descendants("text", &texts)
	stage := ""
	for _, t := range texts {
		if m := stageRE.FindStringSubmatch(t.joinedText()); m != nil {
			stage = strings.Join(strings.Fields(t.joinedText()), " ")
			break
		}
	}
	var buf bytes.Buffer
	for _, k := range root.kids {
		writeDChild(&buf, k)
	}
	return buf.String(), removed, stage, nil
}

func houseQName(name xml.Name) string {
	if p, ok := houseNamespaces[name.Space]; ok {
		return p + ":" + name.Local
	}
	return name.Local
}

func writeDChild(buf *bytes.Buffer, k dChild) {
	if k.el == nil {
		_ = xml.EscapeText(buf, []byte(k.text))
		return
	}
	n := k.el
	buf.WriteByte('<')
	buf.WriteString(houseQName(n.name))
	for _, a := range n.attrs {
		if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
			continue
		}
		buf.WriteByte(' ')
		buf.WriteString(houseQName(a.Name))
		buf.WriteString(`="`)
		_ = xml.EscapeText(buf, []byte(a.Value))
		buf.WriteByte('"')
	}
	if len(n.kids) == 0 {
		buf.WriteString("/>")
		return
	}
	buf.WriteByte('>')
	for _, c := range n.kids {
		writeDChild(buf, c)
	}
	buf.WriteString("</")
	buf.WriteString(houseQName(n.name))
	buf.WriteByte('>')
}

// --- house.svg ------------------------------------------------------------

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func num(v float64) string {
	v = math.Round(v*1e4) / 1e4
	if v == 0 {
		v = 0 // drop the sign of negative zero
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

type pt struct{ x, y float64 }

// orderedSeats sorts seats of a row by number: numeric when all numbers are
// integers, otherwise as text; ties by key.
func orderedSeats(seats []seating.Seat) []seating.Seat {
	out := append([]seating.Seat(nil), seats...)
	allInt := true
	for _, s := range out {
		if _, err := strconv.Atoi(strings.TrimSpace(s.Number)); err != nil {
			allInt = false
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if allInt {
			a, _ := strconv.Atoi(strings.TrimSpace(out[i].Number))
			b, _ := strconv.Atoi(strings.TrimSpace(out[j].Number))
			if a != b {
				return a < b
			}
		} else if out[i].Number != out[j].Number {
			return out[i].Number < out[j].Number
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func seatFontSize(r float64, number string) float64 {
	n := len([]rune(number))
	size := r * 1.3
	switch {
	case n >= 4:
		size = r * 0.7
	case n == 3:
		size = r * 0.9
	}
	return size
}

func textElem(x, y, size float64, content string) string {
	return fmt.Sprintf(`<text x="%s" y="%s" style="font-family:sans-serif;font-size:%spx;fill:#000000;text-anchor:middle;dominant-baseline:central;pointer-events:none">%s</text>`,
		num(x), num(y), num(size), esc(content))
}

// rowEnds returns the label positions left and right of a row, the unit
// direction pointing outward at each end, and the mean seat pitch.
func rowEnds(seats []seating.Seat, scale float64) (left, right, dirLeft, dirRight pt, pitch float64) {
	first, last := seats[0], seats[len(seats)-1]
	a := pt{first.X * scale, first.Y * scale}
	b := pt{last.X * scale, last.Y * scale}
	r := first.Radius * scale
	if len(seats) == 1 {
		d := rowLabelPitches * 2 * r
		return pt{a.x - d, a.y}, pt{a.x + d, a.y}, pt{-1, 0}, pt{1, 0}, 2 * r
	}
	if a.x > b.x || (a.x == b.x && a.y > b.y) {
		a, b = b, a
	}
	pitchSum := 0.0
	for i := 1; i < len(seats); i++ {
		pitchSum += math.Hypot((seats[i].X-seats[i-1].X)*scale, (seats[i].Y-seats[i-1].Y)*scale)
	}
	pitch = pitchSum / float64(len(seats)-1)
	if pitch <= 0 {
		pitch = 2 * r
	}
	length := math.Hypot(b.x-a.x, b.y-a.y)
	if length == 0 {
		return pt{a.x - rowLabelPitches*pitch, a.y}, pt{b.x + rowLabelPitches*pitch, b.y}, pt{-1, 0}, pt{1, 0}, pitch
	}
	dx, dy := (b.x-a.x)/length, (b.y-a.y)/length
	d := rowLabelPitches * pitch
	return pt{a.x - dx*d, a.y - dy*d}, pt{b.x + dx*d, b.y + dy*d}, pt{-dx, -dy}, pt{dx, dy}, pitch
}

// --- merge guard ----------------------------------------------------------

// The importers key sectors and rows with a slug that keeps only a-z0-9, so
// names that differ only in other characters collapse into one. mergeGuard
// counts the raw names in the source itself and compares them with the
// imported geometry.

type rawEl struct {
	name  xml.Name
	attrs []xml.Attr
	kids  []*rawEl
	text  string // character data directly inside the element
}

func parseRaw(source []byte) (*rawEl, error) {
	dec := xml.NewDecoder(bytes.NewReader(source))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	var root *rawEl
	var stack []*rawEl
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &rawEl{name: t.Name, attrs: append([]xml.Attr(nil), t.Attr...)}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("no element found")
	}
	return root, nil
}

func (n *rawEl) sbt(local string) string {
	for _, a := range n.attrs {
		if a.Name.Local == local && (a.Name.Space == seating.SBTNamespaceURI || a.Name.Space == "sbt") {
			return a.Value
		}
	}
	return ""
}

func (n *rawEl) plain(local string) string {
	for _, a := range n.attrs {
		if a.Name.Local == local && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}

func (n *rawEl) label() string {
	for _, a := range n.attrs {
		if a.Name.Local == "label" && (a.Name.Space == "" || strings.Contains(a.Name.Space, "inkscape")) {
			return a.Value
		}
	}
	return ""
}

func (n *rawEl) titleText() string {
	for _, k := range n.kids {
		if k.name.Local == "title" {
			return strings.TrimSpace(k.text)
		}
	}
	return ""
}

var (
	rawSectorPrefixRE = regexp.MustCompile(`(?i)^\s*(?:сектор|sector)\s+`)
	rawGALabelRE      = regexp.MustCompile(`(?i)^\s*#ga\s+`)
)

// slugKey is the importers' normalizeKey: only a-z and 0-9 survive.
func slugKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

type rawNames struct {
	sectors map[string]bool
	rows    map[[2]string]bool
	seats   int
}

func collectRawNames(root *rawEl, dialect string) rawNames {
	out := rawNames{sectors: map[string]bool{}, rows: map[[2]string]bool{}}
	if dialect == "sbt" || dialect == "sbt-template" {
		template := dialect == "sbt-template"
		var walk func(n *rawEl, sector, row string)
		walk = func(n *rawEl, sector, row string) {
			if n.name.Local == "metadata" {
				return
			}
			if s := strings.TrimSpace(n.sbt("sect")); s != "" {
				sector = s
			}
			if r := strings.TrimSpace(n.sbt("row")); r != "" {
				row = r
			} else if n.sbt("sect") == "" && n.name.Local == "g" {
				if t := n.titleText(); t != "" {
					row = t
				}
			}
			isSeat := n.sbt("id") != "" && n.sbt("state") != ""
			if template {
				isSeat = n.sbt("seat") != "" && sector != ""
			}
			if n.name.Local == "circle" && isSeat {
				out.seats++
				out.sectors[sector] = true
				out.rows[[2]string{sector, row}] = true
				return
			}
			for _, k := range n.kids {
				walk(k, sector, row)
			}
		}
		walk(root, "", "")
		return out
	}
	var walk func(n *rawEl)
	walk = func(n *rawEl) {
		if id := n.plain("id"); id == "PriceCategory" || id == "Legend" {
			return
		}
		label := n.label()
		if rawGALabelRE.MatchString(label) {
			return
		}
		if n != root && strings.HasPrefix(strings.TrimSpace(label), "#") {
			l := strings.TrimPrefix(strings.TrimSpace(label), "#")
			sector := strings.TrimSpace(rawSectorPrefixRE.ReplaceAllString(l, ""))
			if sector == "" {
				sector = l
			}
			out.sectors[sector] = true
			out.rows[[2]string{sector, n.titleText()}] = true
			for _, k := range n.kids {
				if k.name.Local == "circle" {
					out.seats++
				}
			}
			return
		}
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(root)
	return out
}

// mergeGuard returns a failure reason (and up to ten colliding names) when
// the imported geometry does not hold exactly the sectors, rows and seats the
// source names.
// With partial set (the import already failed) only name collisions count.
func mergeGuard(source []byte, dialect string, g seating.Geometry, partial bool) (string, []string) {
	root, err := parseRaw(source)
	if err != nil {
		return "source could not be scanned for sector and row names: " + err.Error(), []string{}
	}
	raw := collectRawNames(root, dialect)
	impSectors, impRows := len(g.Sections), 0
	emptyKey := false
	for _, sec := range g.Sections {
		impRows += len(sec.Rows)
		if sec.Key == "" {
			emptyKey = true
		}
		for _, r := range sec.Rows {
			if r.Key == "" {
				emptyKey = true
			}
		}
	}
	if !partial && len(raw.sectors) == impSectors && len(raw.rows) == impRows && !emptyKey && raw.seats == g.SeatCount() {
		return "", nil
	}

	if !partial && dialect == "sbt-template" && len(raw.sectors) == impSectors && len(raw.rows) == impRows && !emptyKey {
		return fmt.Sprintf("importer skipped template seats: %d in the source, %d imported (a seat without sbt:cat?)",
			raw.seats, g.SeatCount()), []string{}
	}

	// Names that share a slug (or have an empty one).
	bySlug := map[string]map[string]bool{}
	add := func(slug, name string) {
		if bySlug[slug] == nil {
			bySlug[slug] = map[string]bool{}
		}
		bySlug[slug][name] = true
	}
	for s := range raw.sectors {
		add("s|"+slugKey(s), s)
	}
	for sr := range raw.rows {
		add("r|"+slugKey(sr[0])+"|"+slugKey(sr[1]), sr[0]+" / "+sr[1])
	}
	set := map[string]bool{}
	for slug, names := range bySlug {
		if len(names) > 1 || strings.HasSuffix(slug, "|") {
			for n := range names {
				set[n] = true
			}
		}
	}
	listedNames := make([]string, 0, len(set))
	for n := range set {
		listedNames = append(listedNames, n)
	}
	if partial && len(listedNames) == 0 {
		return "", nil
	}
	sort.Strings(listedNames)
	if len(listedNames) > 10 {
		listedNames = listedNames[:10]
	}
	reason := fmt.Sprintf("importer merged sectors or rows: %d sectors and %d rows in the source, %d and %d imported (names that differ only in non-Latin characters)",
		len(raw.sectors), len(raw.rows), impSectors, impRows)
	return reason, listedNames
}

// --- sbt template ---------------------------------------------------------

// The Forzum Editor saves a TEMPLATE: seats carry sbt:seat and sbt:cat but no
// sbt:id and no sbt:state, and categories have no name and no price.

var ordinalNames = []string{"First", "Second", "Third", "Fourth", "Fifth", "Sixth", "Seventh", "Eighth",
	"Ninth", "Tenth", "Eleventh", "Twelfth", "Thirteenth", "Fourteenth", "Fifteenth"}

var (
	seatPrefixRE  = regexp.MustCompile(`\s([\w.-]+):seat\s*=`)
	indexPrefixRE = regexp.MustCompile(`\s([\w.-]+):index\s*=`)
)

func isTemplate(root *rawEl) bool {
	hasSect, hasSeat, hasBoth := false, false, false
	var walk func(n *rawEl)
	walk = func(n *rawEl) {
		if strings.TrimSpace(n.sbt("sect")) != "" {
			hasSect = true
		}
		if n.name.Local == "circle" {
			if n.sbt("seat") != "" {
				hasSeat = true
			}
			if n.sbt("id") != "" && n.sbt("state") != "" {
				hasBoth = true
			}
		}
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(root)
	return hasSect && hasSeat && !hasBoth
}

type insertion struct {
	pos  int
	text string
}

// adaptTemplate returns a copy of an sbt template with the attributes the
// importer needs inserted into the start tags. Every other byte is kept. The
// second result is false when source is not a template.
func adaptTemplate(source []byte) ([]byte, bool) {
	root, err := parseRaw(source)
	if err != nil || !isTemplate(root) {
		return nil, false
	}
	dec := xml.NewDecoder(bytes.NewReader(source))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	isSBT := func(a xml.Attr, local string) bool {
		return a.Name.Local == local && (a.Name.Space == seating.SBTNamespaceURI || a.Name.Space == "sbt")
	}
	var edits []insertion
	var stack []bool
	sectDepth, nextID := 0, 1
	for {
		start := int(dec.InputOffset())
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, false
		}
		end := int(dec.InputOffset())
		switch t := tok.(type) {
		case xml.StartElement:
			own := false
			has := map[string]bool{}
			for _, a := range t.Attr {
				if isSBT(a, "sect") && strings.TrimSpace(a.Value) != "" {
					own = true
				}
				for _, l := range []string{"seat", "cat", "name", "price", "id", "state"} {
					if isSBT(a, l) {
						has[l] = true
					}
				}
			}
			if end > len(source) || start < 0 || start >= end {
				return nil, false
			}
			tag := source[start:end]
			nameEnd := bytes.IndexAny(tag, " \t\r\n/>")
			if nameEnd < 0 {
				return nil, false
			}
			switch {
			case t.Name.Local == "circle" && sectDepth > 0 && has["seat"] && has["cat"]:
				prefix := "sbt"
				if m := seatPrefixRE.FindSubmatch(tag); m != nil {
					prefix = string(m[1])
				}
				text := ""
				if !has["id"] {
					text += fmt.Sprintf(` %s:id="%d"`, prefix, nextID)
				}
				if !has["state"] {
					text += fmt.Sprintf(` %s:state="1"`, prefix)
				}
				nextID++
				if text != "" {
					edits = append(edits, insertion{start + nameEnd, text})
				}
			case t.Name.Local == "category" && (t.Name.Space == seating.SBTNamespaceURI || t.Name.Space == "sbt"):
				prefix := "sbt"
				if m := indexPrefixRE.FindSubmatch(tag); m != nil {
					prefix = string(m[1])
				}
				idx := 0
				for _, a := range t.Attr {
					if isSBT(a, "index") {
						idx, _ = strconv.Atoi(strings.TrimSpace(a.Value))
					}
				}
				text := ""
				if !has["name"] && idx >= 1 && idx <= len(ordinalNames) {
					text += fmt.Sprintf(` %s:name="%s"`, prefix, ordinalNames[idx-1])
				}
				if !has["price"] {
					text += fmt.Sprintf(` %s:price="0"`, prefix)
				}
				if text != "" {
					edits = append(edits, insertion{start + nameEnd, text})
				}
			}
			if own {
				sectDepth++
			}
			stack = append(stack, own)
		case xml.EndElement:
			if len(stack) > 0 {
				if stack[len(stack)-1] {
					sectDepth--
				}
				stack = stack[:len(stack)-1]
			}
		}
	}
	var out bytes.Buffer
	last := 0
	for _, e := range edits {
		out.Write(source[last:e.pos])
		out.WriteString(e.text)
		last = e.pos
	}
	out.Write(source[last:])
	return out.Bytes(), true
}

// --- label placement ------------------------------------------------------

type box struct{ x0, y0, x1, y1 float64 }

func (a box) overlaps(b box) bool {
	return a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1
}

type circ struct{ x, y, r float64 }

func (b box) hitsCircle(c circ) bool {
	nx := math.Max(b.x0, math.Min(c.x, b.x1))
	ny := math.Max(b.y0, math.Min(c.y, b.y1))
	return math.Hypot(c.x-nx, c.y-ny) < c.r
}

func textBox(cx, cy, size float64, content string) box {
	w := 0.6 * size * float64(len([]rune(content)))
	return box{cx - w/2, cy - size/2, cx + w/2, cy + size/2}
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func normText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func rowNamePlaceholder(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "no", "-", "—", "0", "без ряда", "нет":
		return true
	}
	return false
}

// affine is an SVG matrix (a b c d e f).
type affine [6]float64

var identity = affine{1, 0, 0, 1, 0, 0}

// mul returns m·n (n is applied first).
func (m affine) mul(n affine) affine {
	return affine{
		m[0]*n[0] + m[2]*n[1], m[1]*n[0] + m[3]*n[1],
		m[0]*n[2] + m[2]*n[3], m[1]*n[2] + m[3]*n[3],
		m[0]*n[4] + m[2]*n[5] + m[4], m[1]*n[4] + m[3]*n[5] + m[5],
	}
}

func (m affine) apply(p pt) pt {
	return pt{m[0]*p.x + m[2]*p.y + m[4], m[1]*p.x + m[3]*p.y + m[5]}
}

var (
	transformFuncRE = regexp.MustCompile(`(\w+)\s*\(([^)]*)\)`)
	numberRE        = regexp.MustCompile(`[-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?`)
)

func parseNumbers(s string) []float64 {
	var out []float64
	for _, m := range numberRE.FindAllString(s, -1) {
		if v, err := strconv.ParseFloat(m, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

func parseTransform(s string) affine {
	m := identity
	for _, f := range transformFuncRE.FindAllStringSubmatch(s, -1) {
		a := parseNumbers(f[2])
		var t affine
		switch f[1] {
		case "matrix":
			if len(a) != 6 {
				continue
			}
			copy(t[:], a)
		case "translate":
			if len(a) == 0 {
				continue
			}
			t = affine{1, 0, 0, 1, a[0], 0}
			if len(a) > 1 {
				t[5] = a[1]
			}
		case "scale":
			if len(a) == 0 {
				continue
			}
			sy := a[0]
			if len(a) > 1 {
				sy = a[1]
			}
			t = affine{a[0], 0, 0, sy, 0, 0}
		case "rotate":
			if len(a) == 0 {
				continue
			}
			rad := a[0] * math.Pi / 180
			c, s := math.Cos(rad), math.Sin(rad)
			t = affine{c, s, -s, c, 0, 0}
			if len(a) == 3 {
				t = affine{1, 0, 0, 1, a[1], a[2]}.mul(t).mul(affine{1, 0, 0, 1, -a[1], -a[2]})
			}
		case "skewX":
			if len(a) == 0 {
				continue
			}
			t = affine{1, 0, math.Tan(a[0] * math.Pi / 180), 1, 0, 0}
		case "skewY":
			if len(a) == 0 {
				continue
			}
			t = affine{1, math.Tan(a[0] * math.Pi / 180), 0, 1, 0, 0}
		default:
			continue
		}
		m = m.mul(t)
	}
	return m
}

var textAnchorRE = regexp.MustCompile(`text-anchor\s*:\s*(\w+)`)

// decorTexts returns the anchor boxes of the decor texts (in house canvas
// units, after the canvas scale) and their normalised contents. The font
// size of each box is fontSize.
func decorTexts(decor string, scale, fontSize float64) ([]box, map[string]bool) {
	names := map[string]bool{}
	if strings.TrimSpace(decor) == "" {
		return nil, names
	}
	root, err := parseFragment(decor)
	if err != nil {
		return nil, names
	}
	var boxes []box
	anchorOf := func(n *dNode, inherited string) string {
		anchor := inherited
		if a := n.attr("text-anchor"); a != "" {
			anchor = a
		}
		if mm := textAnchorRE.FindStringSubmatch(n.attr("style")); mm != nil {
			anchor = mm[1]
		}
		return anchor
	}
	addBox := func(m affine, x, y float64, content, anchor string) {
		p := m.apply(pt{x, y})
		p = pt{p.x * scale, p.y * scale}
		w := 0.6 * fontSize * float64(len([]rune(content)))
		var x0, x1 float64
		switch anchor {
		case "start":
			x0, x1 = p.x, p.x+w
		case "middle":
			x0, x1 = p.x-w/2, p.x+w/2
		case "end":
			x0, x1 = p.x-w, p.x
		default: // not stated: a CSS class may centre it, so cover both
			x0, x1 = p.x-w/2, p.x+w
		}
		boxes = append(boxes, box{x0, p.y - fontSize, x1, p.y + 0.2*fontSize})
	}
	var walk func(n *dNode, m affine, inherited string)
	walk = func(n *dNode, m affine, inherited string) {
		if t := n.attr("transform"); t != "" {
			m = m.mul(parseTransform(t))
		}
		anchor := anchorOf(n, inherited)
		if n.name.Local == "text" {
			content := n.joinedText()
			if content == "" {
				return
			}
			names[normText(content)] = true
			var spans []*dNode
			n.descendants("tspan", &spans)
			positioned := false
			for _, s := range spans {
				if s.attr("x") != "" || s.attr("y") != "" {
					positioned = true
					break
				}
			}
			x, y := firstNumber(n.attr("x")), firstNumber(n.attr("y"))
			if !positioned {
				addBox(m, x, y, content, anchor)
				return
			}
			for _, s := range spans {
				sc := s.joinedText()
				if sc == "" {
					continue
				}
				sx, sy := x, y
				if s.attr("x") != "" {
					sx = firstNumber(s.attr("x"))
				}
				if s.attr("y") != "" {
					sy = firstNumber(s.attr("y"))
				}
				addBox(m, sx, sy, sc, anchorOf(s, anchor))
			}
			return
		}
		for _, k := range n.kids {
			if k.el != nil {
				walk(k.el, m, anchor)
			}
		}
	}
	for _, k := range root.kids {
		if k.el != nil {
			walk(k.el, identity, "")
		}
	}
	return boxes, names
}

func firstNumber(s string) float64 {
	if v := parseNumbers(s); len(v) > 0 {
		return v[0]
	}
	return 0
}

type sectorInfo struct {
	name                   string
	rows                   []rowInfo
	minX, minY, maxX, maxY float64
	medR                   float64
}

type rowInfo struct {
	name  string
	seats []seating.Seat
}

// buildHouse writes house.svg. The second result lists the review reasons of
// the label placement.
func buildHouse(g seating.Geometry, cats []seating.Category, decor string, scale float64) ([]byte, []string) {
	var b bytes.Buffer
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" xmlns:svg="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" xmlns:sodipodi="http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd" viewBox="0 0 %s %s">`+"\n",
		num(g.Canvas.Width*scale), num(g.Canvas.Height*scale))

	colorOf := map[int]string{}
	b.WriteString(`<g id="PriceCategory">` + "\n")
	for i, c := range cats {
		colorOf[c.Index] = c.Color
		fmt.Fprintf(&b, `<circle inkscape:label="#%s" cx="%d" cy="-10" r="4" style="fill:%s"/>`+"\n",
			esc(c.Name), -10-12*i, esc(c.Color))
	}
	b.WriteString("</g>\n")
	b.WriteString(`<g id="Legend"/>` + "\n")

	// Pass 1: row groups and seat circles.
	var sectors []sectorInfo
	var seatCircles []circ
	var allR []float64
	for _, sec := range g.Sections {
		si := sectorInfo{name: sec.Name, minX: math.Inf(1), minY: math.Inf(1), maxX: math.Inf(-1), maxY: math.Inf(-1)}
		var radii []float64
		for _, row := range sec.Rows {
			seats := orderedSeats(row.Seats)
			fmt.Fprintf(&b, `<g inkscape:label="#Сектор %s">`+"\n<title>%s</title>\n", esc(sec.Name), esc(row.Name))
			for _, s := range seats {
				fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="%s" style="fill:%s" data-sector="%s" data-row="%s" data-seat="%s"><title>%s</title></circle>`+"\n",
					num(s.X*scale), num(s.Y*scale), num(s.Radius*scale), esc(colorOf[s.CategoryIndex]),
					esc(sec.Name), esc(row.Name), esc(s.Number), esc(s.Number))
				x, y, r := s.X*scale, s.Y*scale, s.Radius*scale
				si.minX = math.Min(si.minX, x-r)
				si.maxX = math.Max(si.maxX, x+r)
				si.minY = math.Min(si.minY, y-r)
				si.maxY = math.Max(si.maxY, y+r)
				radii = append(radii, r)
				allR = append(allR, r)
				seatCircles = append(seatCircles, circ{x, y, r})
			}
			b.WriteString("</g>\n")
			if len(seats) > 0 {
				si.rows = append(si.rows, rowInfo{name: row.Name, seats: seats})
			}
		}
		si.medR = median(radii)
		sectors = append(sectors, si)
	}

	decorBoxes, decorNames := decorTexts(decor, scale, 2*median(allR))
	var review []string
	var generated []box // kept row labels and placed titles
	free := func(bx box) bool {
		for _, c := range seatCircles {
			if bx.hitsCircle(c) {
				return false
			}
		}
		for _, d := range decorBoxes {
			if bx.overlaps(d) {
				return false
			}
		}
		return true
	}
	freeOfLabels := func(bx box) bool {
		for _, o := range generated {
			if bx.overlaps(o) {
				return false
			}
		}
		return true
	}

	// Pass 2: seat numbers and row labels.
	secLabels := make([]bytes.Buffer, len(sectors))
	for i, si := range sectors {
		out := &secLabels[i]
		for _, row := range si.rows {
			for _, s := range row.seats {
				out.WriteString(textElem(s.X*scale, s.Y*scale, seatFontSize(s.Radius*scale, s.Number), s.Number))
				out.WriteByte('\n')
			}
			if rowNamePlaceholder(row.name) {
				continue
			}
			left, right, dl, dr, pitch := rowEnds(row.seats, scale)
			size := row.seats[0].Radius * scale * 1.4
			for _, side := range []struct {
				name string
				at   pt
				dir  pt
			}{{"left", left, dl}, {"right", right, dr}} {
				placed := false
				for k := 0; k <= 3 && !placed; k++ {
					p := pt{side.at.x + side.dir.x*pitch*float64(k), side.at.y + side.dir.y*pitch*float64(k)}
					bx := textBox(p.x, p.y, size, row.name)
					if !free(bx) {
						continue
					}
					out.WriteString(textElem(p.x, p.y, size, row.name))
					out.WriteByte('\n')
					generated = append(generated, bx)
					placed = true
				}
				if !placed {
					review = append(review, fmt.Sprintf("row label dropped: %s/%s %s", si.name, row.name, side.name))
				}
			}
		}
	}

	// Pass 3: sector titles.
	for i, si := range sectors {
		if len(si.rows) == 0 || decorNames[normText(si.name)] {
			continue
		}
		size := 2.0 * si.medR
		w := 0.6 * size * float64(len([]rune(si.name)))
		gap := si.medR
		cx := (si.minX + si.maxX) / 2
		above := si.minY - gap - size
		cands := []box{
			{cx - w/2, above, cx + w/2, above + size},
			{cx - w/2, si.maxY + gap, cx + w/2, si.maxY + gap + size},
			{si.minX, above, si.minX + w, above + size},
			{si.maxX - w, above, si.maxX, above + size},
		}
		chosen := -1
		for ci, c := range cands {
			if free(c) && freeOfLabels(c) {
				chosen = ci
				break
			}
		}
		if chosen < 0 {
			chosen = 0
			review = append(review, "sector title overlaps: "+si.name)
		}
		c := cands[chosen]
		generated = append(generated, c)
		secLabels[i].WriteString(textElem((c.x0+c.x1)/2, (c.y0+c.y1)/2, size, si.name))
		secLabels[i].WriteByte('\n')
	}
	var labels bytes.Buffer
	for i := range secLabels {
		labels.Write(secLabels[i].Bytes())
	}

	b.WriteString(`<g id="HouseDecor">`)
	if scale != 1 {
		fmt.Fprintf(&b, `<g transform="scale(%s)">`, strconv.FormatFloat(scale, 'f', -1, 64))
		b.WriteString(decor)
		b.WriteString("</g>")
	} else {
		b.WriteString(decor)
	}
	b.WriteString("</g>\n")
	b.WriteString(`<g id="HouseLabels" style="pointer-events:none">` + "\n")
	b.Write(labels.Bytes())
	b.WriteString("</g>\n</svg>\n")
	return b.Bytes(), review
}
