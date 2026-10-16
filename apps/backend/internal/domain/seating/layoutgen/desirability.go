// desirability.go is the first algorithm (spec 26 §4.1, steps 1 to 5):
//
//  1. Every seat gets a desirability score from its distance to the stage
//     (normalised over the hall), its angle from the stage axis, its row's
//     rank inside the section (nearer rows first), the section weight and an
//     obstructed-view penalty.
//  2. The hall is cut into categories by the share template, over whole
//     rows (step 3: no islands, no chequerboard): the rows sorted by their
//     mean score fill the dearest category up to its share, then the next.
//  3. With the option "cheaper_row_edges" a long row is cut into a middle
//     and two edges first, and an edge never ends up dearer than its own
//     middle — the geometric rule the owner asked for separately from the
//     share templates (spec 26 §4.1, addition of 2026-10-15).
//  4. Prices come from the ladder of the common parameters.
//  5. Evaluate gives the revenue estimate.
//
// The whole hall, frozen seats included, takes part in the cut, so a
// proposal made after sales started cuts the hall the way a fresh one would
// and only the free seats move. Deterministic: no seed, ties broken by key.
package layoutgen

import (
	"math"
	"sort"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// DesirabilityID is the registry ID of the algorithm.
const DesirabilityID = "desirability"

// ParamCheaperRowEdges is the algorithm's own option.
const ParamCheaperRowEdges = "cheaper_row_edges"

// Weights of the score. They add up to 1, so a score is in [0, 1].
const (
	weightDistance = 0.55
	weightAngle    = 0.25
	weightRow      = 0.20
)

const (
	// sectionWeightFloor is what a section of weight 0 keeps of its
	// score: weight w multiplies the score by floor + (1-floor)*w.
	sectionWeightFloor = 0.25
	// obstructedFactor multiplies the score of an obstructed seat.
	obstructedFactor = 0.5
	// edgeMinSeats is the shortest row cut into middle and edges.
	edgeMinSeats = 8
	// edgeFraction: a seat is an edge seat when it is further from the
	// row's centre than this fraction of the half-length of the row.
	edgeFraction = 0.5
	// edgeFactor lowers the score of an edge segment so it sorts after
	// the middle of its row.
	edgeFactor = 0.9
)

func init() { Register(Desirability{}) }

// Desirability is the algorithm. The zero value is ready to use.
type Desirability struct{}

// ID implements Algorithm.
func (Desirability) ID() string { return DesirabilityID }

// Params implements Algorithm.
func (Desirability) Params() ParamSchema {
	params := CommonParams()
	params = append(params, Param{
		Name: ParamCheaperRowEdges, Type: TypeBoolean, Default: false,
		LabelKey: "layoutgen.param.cheaper_row_edges", HintKey: "layoutgen.param.cheaper_row_edges.hint",
	})
	return ParamSchema{
		LabelKey:            "layoutgen.algorithm.desirability",
		DescriptionKey:      "layoutgen.algorithm.desirability.description",
		RequiredAnnotations: []string{AnnotationFocalPoint},
		Params:              params,
	}
}

// unit is a group of seats that always share a category: a whole row, or
// the middle or one edge of a long row.
type unit struct {
	seats []int // indexes into the flattened seat list
	score float64
	first string // smallest seat key, the tie-breaker
	row   int    // global row number
	edge  bool
	slot  int
}

// Suggest implements Algorithm.
func (Desirability) Suggest(g seating.Geometry, a Annotations, st SessionState, p Values) (Layout, Metrics, error) {
	plan, err := PlanSlots(p, st.Currency)
	if err != nil {
		return Layout{}, Metrics{}, err
	}
	if a.FocalPoint == nil {
		return Layout{}, Metrics{}, &AnnotationRequiredError{Annotation: AnnotationFocalPoint}
	}
	seats := Seats(g)
	if len(seats) == 0 {
		return Layout{}, Metrics{}, ErrNoSeats
	}
	scores := seatScores(g, seats, a)
	units := buildUnits(g, seats, scores, p.Bool(ParamCheaperRowEdges))
	cutUnits(units, plan.Shares, len(seats))

	l := Layout{Slots: plan.Slots(), Assignments: make(map[string]string, len(seats))}
	for _, u := range units {
		for _, i := range u.seats {
			key := seats[i].Key
			if _, frozen := st.SoldHeld[key]; frozen {
				continue
			}
			l.Assignments[key] = l.Slots[u.slot].Key
		}
	}
	return l, Evaluate(g, l, st), nil
}

// seatScores computes the desirability of every seat, higher is better.
func seatScores(g seating.Geometry, seats []PlacedSeat, a Annotations) []float64 {
	fx, fy := a.FocalPoint.X, a.FocalPoint.Y

	// The stage axis points from the stage to the centre of the seats.
	var cx, cy float64
	for _, s := range seats {
		cx += s.X
		cy += s.Y
	}
	cx /= float64(len(seats))
	cy /= float64(len(seats))
	ax, ay := cx-fx, cy-fy
	if l := math.Hypot(ax, ay); l > 1e-9 {
		ax, ay = ax/l, ay/l
	} else {
		ax, ay = 0, 1
	}

	dist := make([]float64, len(seats))
	dMin, dMax := math.Inf(1), math.Inf(-1)
	for i, s := range seats {
		dist[i] = math.Hypot(s.X-fx, s.Y-fy)
		dMin = math.Min(dMin, dist[i])
		dMax = math.Max(dMax, dist[i])
	}

	rowRank := rowRanks(g, seats, dist)
	obstructed := make(map[string]bool, len(a.ObstructedSeatKeys))
	for _, k := range a.ObstructedSeatKeys {
		obstructed[k] = true
	}

	out := make([]float64, len(seats))
	for i, s := range seats {
		var distN float64
		if dMax-dMin > 1e-9 {
			distN = (dist[i] - dMin) / (dMax - dMin)
		}
		var angN float64
		if dist[i] > 1e-9 {
			cos := ((s.X-fx)*ax + (s.Y-fy)*ay) / dist[i]
			cos = math.Max(-1, math.Min(1, cos))
			angN = math.Min(math.Acos(cos)/(math.Pi/2), 1)
		}
		score := 1 - (weightDistance*distN + weightAngle*angN + weightRow*rowRank[i])
		w := 1.0
		if sw, ok := a.SectionWeights[g.Sections[s.Section].Key]; ok {
			w = sw
		}
		score *= sectionWeightFloor + (1-sectionWeightFloor)*w
		if obstructed[s.Key] {
			score *= obstructedFactor
		}
		out[i] = score
	}
	return out
}

// rowRanks ranks every row inside its section by its mean distance to the
// stage, 0 for the nearest and 1 for the furthest, and returns the rank of
// each seat's row.
func rowRanks(g seating.Geometry, seats []PlacedSeat, dist []float64) []float64 {
	type rowStat struct {
		sum float64
		n   int
	}
	stats := make([][]rowStat, len(g.Sections))
	for si, sec := range g.Sections {
		stats[si] = make([]rowStat, len(sec.Rows))
	}
	for i, s := range seats {
		rs := &stats[s.Section][s.Row]
		rs.sum += dist[i]
		rs.n++
	}
	rank := make([][]float64, len(g.Sections))
	for si := range g.Sections {
		order := make([]int, 0, len(stats[si]))
		for ri := range stats[si] {
			if stats[si][ri].n > 0 {
				order = append(order, ri)
			}
		}
		mean := func(ri int) float64 { return stats[si][ri].sum / float64(stats[si][ri].n) }
		sort.SliceStable(order, func(x, y int) bool {
			mx, my := mean(order[x]), mean(order[y])
			if mx != my {
				return mx < my
			}
			return order[x] < order[y] // rows are in key order already
		})
		rank[si] = make([]float64, len(stats[si]))
		for pos, ri := range order {
			if len(order) > 1 {
				rank[si][ri] = float64(pos) / float64(len(order)-1)
			}
		}
	}
	out := make([]float64, len(seats))
	for i, s := range seats {
		out[i] = rank[s.Section][s.Row]
	}
	return out
}

// buildUnits groups the seats into rows, or into middles and edges of long
// rows when edges is true.
func buildUnits(g seating.Geometry, seats []PlacedSeat, scores []float64, edges bool) []*unit {
	// Seats of one row are contiguous in the flattened list.
	var units []*unit
	rowNo := 0
	start := 0
	for start < len(seats) {
		end := start
		for end < len(seats) && seats[end].Section == seats[start].Section && seats[end].Row == seats[start].Row {
			end++
		}
		idx := make([]int, 0, end-start)
		for i := start; i < end; i++ {
			idx = append(idx, i)
		}
		if edges && len(idx) >= edgeMinSeats {
			left, mid, right := splitRow(seats, idx)
			for _, part := range []struct {
				seats []int
				edge  bool
			}{{mid, false}, {left, true}, {right, true}} {
				if len(part.seats) > 0 {
					units = append(units, newUnit(seats, scores, part.seats, rowNo, part.edge))
				}
			}
		} else {
			units = append(units, newUnit(seats, scores, idx, rowNo, false))
		}
		rowNo++
		start = end
	}
	return units
}

func newUnit(seats []PlacedSeat, scores []float64, idx []int, row int, edge bool) *unit {
	u := &unit{seats: idx, row: row, edge: edge}
	for _, i := range idx {
		u.score += scores[i]
		if u.first == "" || seats[i].Key < u.first {
			u.first = seats[i].Key
		}
	}
	u.score /= float64(len(idx))
	if edge {
		u.score *= edgeFactor
	}
	return u
}

// splitRow orders a row's seats along the row's own direction (its
// principal axis, so a curved or rotated row works too) and cuts it into a
// left edge, a middle and a right edge.
func splitRow(seats []PlacedSeat, idx []int) (left, mid, right []int) {
	var mx, my float64
	for _, i := range idx {
		mx += seats[i].X
		my += seats[i].Y
	}
	mx /= float64(len(idx))
	my /= float64(len(idx))
	var sxx, sxy, syy float64
	for _, i := range idx {
		dx, dy := seats[i].X-mx, seats[i].Y-my
		sxx += dx * dx
		sxy += dx * dy
		syy += dy * dy
	}
	// Direction of the larger eigenvalue of the covariance matrix.
	theta := 0.5 * math.Atan2(2*sxy, sxx-syy)
	ux, uy := math.Cos(theta), math.Sin(theta)

	t := make(map[int]float64, len(idx))
	tMin, tMax := math.Inf(1), math.Inf(-1)
	for _, i := range idx {
		v := (seats[i].X-mx)*ux + (seats[i].Y-my)*uy
		t[i] = v
		tMin = math.Min(tMin, v)
		tMax = math.Max(tMax, v)
	}
	centre := (tMin + tMax) / 2
	half := (tMax - tMin) / 2
	for _, i := range idx {
		switch d := t[i] - centre; {
		case half > 1e-9 && d < -edgeFraction*half:
			left = append(left, i)
		case half > 1e-9 && d > edgeFraction*half:
			right = append(right, i)
		default:
			mid = append(mid, i)
		}
	}
	return left, mid, right
}

// cutUnits gives every unit a slot. Units sorted by score fill the slots in
// order: a unit belongs to the first slot whose cumulative target share of
// the hall is beyond the unit's own midpoint. An edge then never stays
// dearer than the middle of its row.
func cutUnits(units []*unit, shares []float64, total int) {
	order := append([]*unit(nil), units...)
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].score != order[j].score {
			return order[i].score > order[j].score
		}
		return order[i].first < order[j].first
	})
	bounds := make([]float64, len(shares))
	var acc float64
	for i, s := range shares {
		acc += s
		bounds[i] = acc * float64(total)
	}
	bounds[len(bounds)-1] = math.Inf(1) // rounding never leaves a unit out

	var filled float64
	slot := 0
	for _, u := range order {
		midpoint := filled + float64(len(u.seats))/2
		for slot < len(bounds)-1 && midpoint >= bounds[slot] {
			slot++
		}
		u.slot = slot
		filled += float64(len(u.seats))
	}

	middle := map[int]int{}
	for _, u := range units {
		if !u.edge {
			middle[u.row] = u.slot
		}
	}
	for _, u := range units {
		if m, ok := middle[u.row]; ok && u.edge && u.slot < m {
			u.slot = m
		}
	}
}
