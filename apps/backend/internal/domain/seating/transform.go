// Package seating — SVG transform resolution shared by both importers.
//
// A plan drawn in Inkscape — and the sbt plan Bil24 serves — positions a
// whole ROW with a transform on its <g>: in Palác Akropolis every one of
// the 90 seats carries the same cy, and the rows are rotated and shifted
// groups. arena renders its own SVG from the stored geometry, so a seat's
// cx/cy must be resolved to the root coordinate system at import time or
// the hall collapses into one line of stacked seats.
package seating

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// affine is the SVG matrix(a b c d e f):
//
//	x' = a·x + c·y + e
//	y' = b·x + d·y + f
type affine [6]float64

var identityAffine = affine{1, 0, 0, 1, 0, 0}

// mul returns m·n — n is applied first, then m, which is how a child's
// transform nests inside its parent's.
func (m affine) mul(n affine) affine {
	return affine{
		m[0]*n[0] + m[2]*n[1],
		m[1]*n[0] + m[3]*n[1],
		m[0]*n[2] + m[2]*n[3],
		m[1]*n[2] + m[3]*n[3],
		m[0]*n[4] + m[2]*n[5] + m[4],
		m[1]*n[4] + m[3]*n[5] + m[5],
	}
}

// apply maps a point into the parent coordinate system.
func (m affine) apply(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// scale is the uniform factor a radius is multiplied by: the square root
// of the area scale, exact for every similarity transform a plan uses.
func (m affine) scale() float64 {
	return math.Sqrt(math.Abs(m[0]*m[3] - m[1]*m[2]))
}

var transformFuncRE = regexp.MustCompile(`([A-Za-z]+)\s*\(([^)]*)\)`)

// parseTransform reads an SVG transform list ("translate(3,4) rotate(90)").
// Functions compose left to right; an unknown function or a malformed
// argument list is skipped, never an error — a plan must not fail to import
// over a transform arena cannot read.
func parseTransform(value string) affine {
	m := identityAffine
	for _, match := range transformFuncRE.FindAllStringSubmatch(value, -1) {
		args, ok := parseTransformArgs(match[2])
		if !ok {
			continue
		}
		t, ok := transformFunc(match[1], args)
		if !ok {
			continue
		}
		m = m.mul(t)
	}
	return m
}

func parseTransformArgs(raw string) ([]float64, bool) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	out := make([]float64, 0, len(fields))
	for _, f := range fields {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

func transformFunc(name string, a []float64) (affine, bool) {
	switch name {
	case "matrix":
		if len(a) != 6 {
			return affine{}, false
		}
		return affine{a[0], a[1], a[2], a[3], a[4], a[5]}, true
	case "translate":
		switch len(a) {
		case 1:
			return affine{1, 0, 0, 1, a[0], 0}, true
		case 2:
			return affine{1, 0, 0, 1, a[0], a[1]}, true
		}
	case "scale":
		switch len(a) {
		case 1:
			return affine{a[0], 0, 0, a[0], 0, 0}, true
		case 2:
			return affine{a[0], 0, 0, a[1], 0, 0}, true
		}
	case "rotate":
		if len(a) != 1 && len(a) != 3 {
			return affine{}, false
		}
		sin, cos := math.Sincos(a[0] * math.Pi / 180)
		r := affine{cos, sin, -sin, cos, 0, 0}
		if len(a) == 3 {
			r = affine{1, 0, 0, 1, a[1], a[2]}.mul(r).mul(affine{1, 0, 0, 1, -a[1], -a[2]})
		}
		return r, true
	case "skewX":
		if len(a) == 1 {
			return affine{1, 0, math.Tan(a[0] * math.Pi / 180), 1, 0, 0}, true
		}
	case "skewY":
		if len(a) == 1 {
			return affine{1, math.Tan(a[0] * math.Pi / 180), 0, 1, 0, 0}, true
		}
	}
	return affine{}, false
}

// resolveTransforms maps every element under root to its cumulative
// transform — its own `transform` nested inside every ancestor's. The root
// <svg> contributes nothing: its viewBox IS the coordinate system the
// geometry is stored in.
func resolveTransforms(root *xmlNode) map[*xmlNode]affine {
	out := map[*xmlNode]affine{}
	if root == nil {
		return out
	}
	var walk func(n *xmlNode, parent affine)
	walk = func(n *xmlNode, parent affine) {
		m := parent
		if n != root {
			if t := strings.TrimSpace(attr(n, "transform")); t != "" {
				m = parent.mul(parseTransform(t))
			}
		}
		out[n] = m
		for _, ch := range n.Children {
			if ch.element != nil {
				walk(ch.element, m)
			}
		}
	}
	walk(root, identityAffine)
	return out
}

// roundCoord trims the float noise a rotation leaves behind (a seat at
// 87.90017 must not become 87.90017000000001), so the geometry checksum
// does not depend on the order the matrices were multiplied in.
func roundCoord(v float64) float64 {
	return math.Round(v*1e5) / 1e5
}

// placeCircle resolves a circle's centre and radius into root coordinates.
// An element absent from the map (or an identity transform) keeps its
// attributes exactly as written.
func placeCircle(transforms map[*xmlNode]affine, el *xmlNode) (x, y, r float64) {
	x = parseDimAttr(attr(el, "cx"))
	y = parseDimAttr(attr(el, "cy"))
	r = parseDimAttr(attr(el, "r"))
	m, ok := transforms[el]
	if !ok || m == identityAffine {
		return x, y, r
	}
	x, y = m.apply(x, y)
	return roundCoord(x), roundCoord(y), roundCoord(r * m.scale())
}
