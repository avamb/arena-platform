package templates

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// V1: the organizer's VIP mark of a ticket is private. No buyer letter
// template mentions it and the template data has no field that could carry it.
func TestTemplates_NeverMentionTheVIPMark(t *testing.T) {
	files, err := filepath.Glob("*.tmpl")
	if err != nil || len(files) == 0 {
		t.Fatalf("no templates found: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(raw)), "vip") {
			t.Errorf("%s mentions VIP: the mark is private to the organizer", f)
		}
	}
}

func TestData_HasNoVIPField(t *testing.T) {
	var walk func(typ reflect.Type, path string)
	seen := map[reflect.Type]bool{}
	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if strings.Contains(strings.ToLower(f.Name), "vip") {
				t.Errorf("%s.%s would carry the organizer's private VIP mark into a letter", path, f.Name)
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	walk(reflect.TypeOf(Data{}), "Data")
}
