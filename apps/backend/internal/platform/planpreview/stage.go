package planpreview

import (
	"encoding/xml"
	"regexp"
	"strconv"
	"strings"
)

// stageRect is the stage of a plan in canvas units.
type stageRect struct {
	x, y, w, h float64
	// word is the text of the stage (may be empty).
	word string
}

// stageWords are the texts that name a stage in a hand-drawn plan.
var stageWords = map[string]bool{
	"stage": true, "scene": true, "scena": true, "scène": true, "escenario": true,
	"bühne": true, "buhne": true, "сцена": true, "сцэна": true, "сцене": true,
}

var translateRe = regexp.MustCompile(`^\s*translate\(\s*([-+0-9.eE]+)[\s,]+([-+0-9.eE]+)\s*\)\s*$`)

// findStage looks for the stage in the decor fragment of the plan. The
// geometry has no stage field: the stage is part of DecorSVG. Two shapes are
// known. The venue generator writes <rect id="decor-stage"> and
// <text id="decor-stage-text">. A hand-drawn plan has a <text> reading "STAGE"
// (or a word of another language) next to a <rect> in the same group. Only
// plain translate() transforms are followed; any other transform on the path
// makes the shape unusable (a guess would put the stage in a wrong place).
func findStage(decor string) (stageRect, bool) {
	if strings.TrimSpace(decor) == "" {
		return stageRect{}, false
	}
	type frame struct {
		tx, ty float64
		ok     bool
		group  int
	}
	type rectInfo struct {
		id         string
		x, y, w, h float64
		group      int
	}
	type textInfo struct {
		id, content string
		group       int
	}
	var rects []rectInfo
	var texts []textInfo
	stack := []frame{{ok: true}}
	nextGroup := 0
	dec := xml.NewDecoder(strings.NewReader(decor))
	dec.Strict = false
	// text collection state
	textDepth, textIdx := 0, -1
	for {
		tok, err := dec.Token()
		if err != nil {
			// io.EOF or malformed decor: use what was read so far.
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			parent := stack[len(stack)-1]
			cur := frame{tx: parent.tx, ty: parent.ty, ok: parent.ok, group: parent.group}
			attr := func(n string) string {
				for _, a := range t.Attr {
					if a.Name.Local == n {
						return a.Value
					}
				}
				return ""
			}
			if tr := strings.TrimSpace(attr("transform")); tr != "" {
				if m := translateRe.FindStringSubmatch(tr); m != nil {
					dx, e1 := strconv.ParseFloat(m[1], 64)
					dy, e2 := strconv.ParseFloat(m[2], 64)
					if e1 == nil && e2 == nil {
						cur.tx += dx
						cur.ty += dy
					} else {
						cur.ok = false
					}
				} else {
					cur.ok = false
				}
			}
			switch t.Name.Local {
			case "g", "svg":
				nextGroup++
				cur.group = nextGroup
			case "rect":
				x, e1 := strconv.ParseFloat(strings.TrimSpace(attr("x")), 64)
				y, e2 := strconv.ParseFloat(strings.TrimSpace(attr("y")), 64)
				w, e3 := strconv.ParseFloat(strings.TrimSpace(attr("width")), 64)
				h, e4 := strconv.ParseFloat(strings.TrimSpace(attr("height")), 64)
				if attr("x") == "" {
					x, e1 = 0, nil
				}
				if attr("y") == "" {
					y, e2 = 0, nil
				}
				if cur.ok && e1 == nil && e2 == nil && e3 == nil && e4 == nil && w > 0 && h > 0 &&
					usable(x+cur.tx, y+cur.ty) && usable(w, h) {
					rects = append(rects, rectInfo{id: attr("id"), x: x + cur.tx, y: y + cur.ty, w: w, h: h, group: cur.group})
				}
			case "text":
				if textDepth == 0 {
					texts = append(texts, textInfo{id: attr("id"), group: cur.group})
					textIdx = len(texts) - 1
				}
			}
			if t.Name.Local == "text" || textDepth > 0 {
				textDepth++
			}
			stack = append(stack, cur)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			if textDepth > 0 {
				textDepth--
				if textDepth == 0 {
					textIdx = -1
				}
			}
		case xml.CharData:
			if textDepth > 0 && textIdx >= 0 {
				texts[textIdx].content += string(t)
			}
		}
	}
	// 1. The venue generator's own marks.
	for _, r := range rects {
		if r.id == "decor-stage" {
			st := stageRect{x: r.x, y: r.y, w: r.w, h: r.h}
			for _, tx := range texts {
				if tx.id == "decor-stage-text" {
					st.word = strings.TrimSpace(tx.content)
				}
			}
			return st, true
		}
	}
	// 2. A stage word with a rectangle in the same group.
	for _, tx := range texts {
		word := strings.TrimSpace(strings.Join(strings.Fields(tx.content), " "))
		if !stageWords[strings.ToLower(word)] {
			continue
		}
		for _, r := range rects {
			if r.group == tx.group {
				return stageRect{x: r.x, y: r.y, w: r.w, h: r.h, word: word}, true
			}
		}
	}
	return stageRect{}, false
}
