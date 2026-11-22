package hscanner

import (
	"fmt"
	"reflect"
)

// hscannerTestRow implements pgx.Row using reflect — safely handles typed nil
// pointer values (e.g. *uuid.UUID(nil)) that appear in the scan destination
// as **uuid.UUID when scanned via &field.
type hscannerTestRow struct {
	vals []any
	err  error
}

func (r *hscannerTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.vals) {
		return fmt.Errorf("hscannerTestRow: %d destinations for %d values", len(dest), len(r.vals))
	}
	for i, v := range r.vals {
		if v == nil {
			continue
		}
		rv := reflect.ValueOf(v)
		dv := reflect.ValueOf(dest[i]).Elem()
		// For nil pointer values (non-nil interface containing nil pointer),
		// set the destination to the zero value of its type.
		if rv.Kind() == reflect.Pointer && rv.IsNil() {
			dv.Set(reflect.Zero(dv.Type()))
			continue
		}
		dv.Set(rv)
	}
	return nil
}
