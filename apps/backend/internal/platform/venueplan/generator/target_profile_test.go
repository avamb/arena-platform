package generator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSubmit_TargetProfile: WithCategoryName sends the default profile with
// only category.name replaced; with no option no target_profile is sent.
func TestSubmit_TargetProfile(t *testing.T) {
	var got []string
	var present []bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		_, ok := r.MultipartForm.Value["target_profile"]
		present = append(present, ok)
		got = append(got, r.FormValue("target_profile"))
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"job_id":"j1"}`)
	}))
	defer ts.Close()
	c := New(ts.URL, testToken)
	if _, err := c.Submit(context.Background(), files[:1], WithCategoryName(" Стандарт ")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(context.Background(), files[:1]); err != nil {
		t.Fatal(err)
	}
	if !present[0] || present[1] {
		t.Fatalf("target_profile present: %v, want [true false]", present)
	}
	var p struct {
		ProfileID      string `json:"profile_id"`
		CommercialData *bool  `json:"commercial_data"`
		Category       struct {
			Name  string `json:"name"`
			Color string `json:"color"`
		} `json:"category"`
	}
	if err := json.Unmarshal([]byte(got[0]), &p); err != nil {
		t.Fatal(err)
	}
	if p.Category.Name != "Стандарт" || p.Category.Color != "#00aaff" || p.ProfileID != "arena-authoring-v1" ||
		p.CommercialData == nil || *p.CommercialData {
		t.Fatalf("profile %s", got[0])
	}
}
