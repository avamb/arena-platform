// params.go describes an algorithm's parameters as data, so the routes, the
// seat-manager component and the bot can draw a form for any algorithm
// without knowing it, and validates a request against that description.
//
// Labels are i18n KEYS (label_key, hint_key), never texts: the clients own
// the words.
package layoutgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

// ParamType is the type of one parameter.
type ParamType string

// Parameter types. A list is a JSON array of integers.
const (
	TypeInteger     ParamType = "integer"
	TypeBoolean     ParamType = "boolean"
	TypeEnum        ParamType = "enum"
	TypeIntegerList ParamType = "integer_list"
)

// Units say how a client should read an integer (or each list item).
const (
	UnitCount         = "count"
	UnitMinorCurrency = "minor_currency" // minor units of the session currency
	UnitPercent       = "percent"
)

// Option is one value of an enum parameter.
type Option struct {
	Value    string `json:"value"`
	LabelKey string `json:"label_key"`
}

// Param is one parameter.
type Param struct {
	Name     string    `json:"name"`
	Type     ParamType `json:"type"`
	Required bool      `json:"required"`
	// Min and Max bound an integer, or every item of an integer list.
	Min *int64 `json:"min,omitempty"`
	Max *int64 `json:"max,omitempty"`
	// MinItems and MaxItems bound the length of a list.
	MinItems int `json:"min_items,omitempty"`
	MaxItems int `json:"max_items,omitempty"`
	// Default is used when the request leaves the parameter out. A
	// parameter without a default and not required is simply absent.
	Default  any      `json:"default"`
	Options  []Option `json:"options,omitempty"`
	Unit     string   `json:"unit,omitempty"`
	LabelKey string   `json:"label_key"`
	HintKey  string   `json:"hint_key,omitempty"`
}

// ParamSchema describes an algorithm to its clients.
type ParamSchema struct {
	LabelKey       string `json:"label_key"`
	DescriptionKey string `json:"description_key,omitempty"`
	// RequiredAnnotations names the annotations Suggest cannot work
	// without (AnnotationFocalPoint, ...). Run refuses before Suggest.
	RequiredAnnotations []string `json:"required_annotations"`
	Params              []Param  `json:"params"`
}

// Values are validated parameters: integers as int64, booleans as bool,
// enums as string, lists as []int64. Defaults are filled in.
type Values map[string]any

// Int returns an integer parameter.
func (v Values) Int(name string) (int64, bool) {
	x, ok := v[name].(int64)
	return x, ok
}

// Bool returns a boolean parameter (false when absent).
func (v Values) Bool(name string) bool {
	x, _ := v[name].(bool)
	return x
}

// String returns an enum parameter.
func (v Values) String(name string) (string, bool) {
	x, ok := v[name].(string)
	return x, ok
}

// Ints returns an integer-list parameter.
func (v Values) Ints(name string) ([]int64, bool) {
	x, ok := v[name].([]int64)
	return x, ok
}

// Validate checks raw request parameters against the schema and returns the
// typed values with defaults filled in. A JSON null counts as left out.
func (s ParamSchema) Validate(raw map[string]json.RawMessage) (Values, error) {
	byName := make(map[string]Param, len(s.Params))
	for _, p := range s.Params {
		byName[p.Name] = p
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names) // the first error must not depend on map order
	for _, name := range names {
		if _, ok := byName[name]; !ok {
			return nil, &ParamError{Param: name, Reason: "unknown parameter"}
		}
	}

	out := Values{}
	for _, p := range s.Params {
		msg, present := raw[p.Name]
		if present && bytes.Equal(bytes.TrimSpace(msg), []byte("null")) {
			present = false
		}
		if !present {
			if p.Default != nil {
				out[p.Name] = p.Default
				continue
			}
			if p.Required {
				return nil, &ParamError{Param: p.Name, Reason: "is required"}
			}
			continue
		}
		val, err := p.decode(msg)
		if err != nil {
			return nil, &ParamError{Param: p.Name, Reason: err.Error()}
		}
		out[p.Name] = val
	}
	return out, nil
}

func (p Param) decode(msg json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(msg))
	dec.UseNumber()
	switch p.Type {
	case TypeInteger:
		var x any
		if err := dec.Decode(&x); err != nil {
			return nil, errors.New("must be an integer")
		}
		// A json.Number target would also take the string "4"; decoding
		// into any keeps strings as strings.
		n, ok := x.(json.Number)
		if !ok {
			return nil, errors.New("must be an integer")
		}
		i, err := n.Int64()
		if err != nil {
			return nil, errors.New("must be an integer")
		}
		if err := p.inRange(i); err != nil {
			return nil, err
		}
		return i, nil
	case TypeBoolean:
		var b bool
		if err := dec.Decode(&b); err != nil {
			return nil, errors.New("must be true or false")
		}
		return b, nil
	case TypeEnum:
		var s string
		if err := dec.Decode(&s); err != nil {
			return nil, errors.New("must be a string")
		}
		for _, o := range p.Options {
			if o.Value == s {
				return s, nil
			}
		}
		return nil, fmt.Errorf("must be one of %s", p.optionList())
	case TypeIntegerList:
		var xs []any
		if err := dec.Decode(&xs); err != nil || xs == nil {
			return nil, errors.New("must be an array of integers")
		}
		if len(xs) < p.MinItems || (p.MaxItems > 0 && len(xs) > p.MaxItems) {
			return nil, fmt.Errorf("must have %d to %d items", p.MinItems, p.MaxItems)
		}
		out := make([]int64, len(xs))
		for i, x := range xs {
			n, ok := x.(json.Number)
			if !ok {
				return nil, errors.New("must be an array of integers")
			}
			v, err := n.Int64()
			if err != nil {
				return nil, errors.New("must be an array of integers")
			}
			if err := p.inRange(v); err != nil {
				return nil, fmt.Errorf("item %d %s", i+1, err.Error())
			}
			out[i] = v
		}
		return out, nil
	}
	return nil, fmt.Errorf("has an unsupported type %q", p.Type)
}

func (p Param) inRange(i int64) error {
	if p.Min != nil && i < *p.Min {
		return fmt.Errorf("must be at least %d", *p.Min)
	}
	if p.Max != nil && i > *p.Max {
		return fmt.Errorf("must be at most %d", *p.Max)
	}
	return nil
}

func (p Param) optionList() string {
	var b bytes.Buffer
	for i, o := range p.Options {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(o.Value)
	}
	return b.String()
}

// check is the registry's sanity check of a schema: names unique, a type
// every parameter can be decoded with, defaults of the declared type and
// inside the declared range.
func (s ParamSchema) check() error {
	if s.LabelKey == "" {
		return errors.New("schema has no label_key")
	}
	seen := map[string]bool{}
	for _, p := range s.Params {
		if p.Name == "" || seen[p.Name] {
			return fmt.Errorf("parameter name %q is empty or repeated", p.Name)
		}
		seen[p.Name] = true
		if p.LabelKey == "" {
			return fmt.Errorf("parameter %q has no label_key", p.Name)
		}
		if p.Default == nil {
			if p.Type == TypeEnum && len(p.Options) == 0 {
				return fmt.Errorf("enum parameter %q has no options", p.Name)
			}
			continue
		}
		buf, err := json.Marshal(p.Default)
		if err != nil {
			return fmt.Errorf("parameter %q: default: %w", p.Name, err)
		}
		got, err := p.decode(buf)
		if err != nil {
			return fmt.Errorf("parameter %q: default %s", p.Name, err.Error())
		}
		if !reflect.DeepEqual(got, p.Default) {
			return fmt.Errorf("parameter %q: default must be stored as its decoded Go type", p.Name)
		}
	}
	return nil
}

// i64 is a helper for the Min/Max pointers.
func i64(v int64) *int64 { return &v }
