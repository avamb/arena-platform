// events_metadata_ab45d_test.go — unit tests for AB-45d: tri-state event
// metadata PATCH semantics (absent=keep, null=clear, value=set).
//
// Feature #448 (AB-45d) coverage:
//   - resolveStr: absent / null / value branches
//   - resolveInt32: absent / null / value branches
//   - optionalString.UnmarshalJSON: absent field / JSON null / string value
//   - optionalInt32.UnmarshalJSON: absent field / JSON null / integer value
//   - JSON round-trip: all 9 metadata fields parsed from a full request body
//
// All tests are pure unit tests — no live PostgreSQL required.
package hcatalog

import (
	"encoding/json"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// resolveStr: absent=keep, null=clear, value=set
// ─────────────────────────────────────────────────────────────────────────────

func TestResolveStr_Absent_KeepsExisting(t *testing.T) {
	existing := ptr("existing-slug")
	opt := optionalString{Present: false, Value: nil}
	got := resolveStr(opt, existing)
	if got != existing {
		t.Errorf("absent: got %v, want existing pointer %v", got, existing)
	}
}

func TestResolveStr_Null_ClearsExisting(t *testing.T) {
	existing := ptr("existing-slug")
	opt := optionalString{Present: true, Value: nil}
	got := resolveStr(opt, existing)
	if got != nil {
		t.Errorf("null: got %v, want nil", got)
	}
}

func TestResolveStr_Value_SetsNew(t *testing.T) {
	existing := ptr("old")
	newVal := ptr("new-slug")
	opt := optionalString{Present: true, Value: newVal}
	got := resolveStr(opt, existing)
	if got == nil || *got != "new-slug" {
		t.Errorf("value: got %v, want \"new-slug\"", got)
	}
}

func TestResolveStr_Absent_WhenExistingIsNil(t *testing.T) {
	opt := optionalString{Present: false}
	got := resolveStr(opt, nil)
	if got != nil {
		t.Errorf("absent + nil existing: got %v, want nil", got)
	}
}

func TestResolveStr_Value_WhenExistingIsNil(t *testing.T) {
	opt := optionalString{Present: true, Value: ptr("hello")}
	got := resolveStr(opt, nil)
	if got == nil || *got != "hello" {
		t.Errorf("value + nil existing: got %v, want \"hello\"", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// resolveInt32: absent=keep, null=clear, value=set
// ─────────────────────────────────────────────────────────────────────────────

func TestResolveInt32_Absent_KeepsExisting(t *testing.T) {
	existing := ptrInt32(120)
	opt := optionalInt32{Present: false}
	got := resolveInt32(opt, existing)
	if got != existing {
		t.Errorf("absent: got %v, want existing pointer %v", got, existing)
	}
}

func TestResolveInt32_Null_ClearsExisting(t *testing.T) {
	existing := ptrInt32(120)
	opt := optionalInt32{Present: true, Value: nil}
	got := resolveInt32(opt, existing)
	if got != nil {
		t.Errorf("null: got %v, want nil", got)
	}
}

func TestResolveInt32_Value_SetsNew(t *testing.T) {
	existing := ptrInt32(60)
	opt := optionalInt32{Present: true, Value: ptrInt32(90)}
	got := resolveInt32(opt, existing)
	if got == nil || *got != 90 {
		t.Errorf("value: got %v, want 90", got)
	}
}

func TestResolveInt32_Absent_WhenExistingIsNil(t *testing.T) {
	opt := optionalInt32{Present: false}
	got := resolveInt32(opt, nil)
	if got != nil {
		t.Errorf("absent + nil existing: got %v, want nil", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// optionalString.UnmarshalJSON
// ─────────────────────────────────────────────────────────────────────────────

func TestOptionalString_UnmarshalJSON_NullValue(t *testing.T) {
	var v optionalString
	if err := json.Unmarshal([]byte(`null`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !v.Present {
		t.Error("Present should be true when JSON null is provided")
	}
	if v.Value != nil {
		t.Errorf("Value should be nil for JSON null, got %v", v.Value)
	}
}

func TestOptionalString_UnmarshalJSON_StringValue(t *testing.T) {
	var v optionalString
	if err := json.Unmarshal([]byte(`"hello-world"`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !v.Present {
		t.Error("Present should be true when a string value is provided")
	}
	if v.Value == nil || *v.Value != "hello-world" {
		t.Errorf("Value should be \"hello-world\", got %v", v.Value)
	}
}

func TestOptionalString_AbsentField_NotPresent(t *testing.T) {
	// When a struct field with optionalString is missing from JSON, UnmarshalJSON
	// is never called — the zero value has Present=false.
	type payload struct {
		Slug optionalString `json:"slug"`
	}
	var p payload
	if err := json.Unmarshal([]byte(`{}`), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Slug.Present {
		t.Error("absent field should have Present=false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// optionalInt32.UnmarshalJSON
// ─────────────────────────────────────────────────────────────────────────────

func TestOptionalInt32_UnmarshalJSON_NullValue(t *testing.T) {
	var v optionalInt32
	if err := json.Unmarshal([]byte(`null`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !v.Present {
		t.Error("Present should be true when JSON null is provided")
	}
	if v.Value != nil {
		t.Errorf("Value should be nil for JSON null, got %v", v.Value)
	}
}

func TestOptionalInt32_UnmarshalJSON_IntValue(t *testing.T) {
	var v optionalInt32
	if err := json.Unmarshal([]byte(`90`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !v.Present {
		t.Error("Present should be true when an integer value is provided")
	}
	if v.Value == nil || *v.Value != 90 {
		t.Errorf("Value should be 90, got %v", v.Value)
	}
}

func TestOptionalInt32_AbsentField_NotPresent(t *testing.T) {
	type payload struct {
		DurationMinutes optionalInt32 `json:"duration_minutes"`
	}
	var p payload
	if err := json.Unmarshal([]byte(`{}`), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.DurationMinutes.Present {
		t.Error("absent field should have Present=false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Full updateEventRequest JSON round-trip: all 9 metadata fields
// ─────────────────────────────────────────────────────────────────────────────

func TestUpdateEventRequest_AllMetadataFields_Value(t *testing.T) {
	body := `{
		"slug": "summer-2026",
		"short_description": "Great event",
		"genre": "festival",
		"age_rating": "18+",
		"duration_minutes": 120,
		"teaser_url": "https://cdn.example/teaser.mp4",
		"trailer_url": "https://cdn.example/trailer.mp4",
		"meta_description": "SEO description",
		"meta_keywords": "festival,music"
	}`
	var req updateEventRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	check := func(name string, opt optionalString, want string) {
		t.Helper()
		if !opt.Present {
			t.Errorf("%s: Present=false, want true", name)
		}
		if opt.Value == nil || *opt.Value != want {
			t.Errorf("%s: got %v, want %q", name, opt.Value, want)
		}
	}
	check("slug", req.Slug, "summer-2026")
	check("short_description", req.ShortDescription, "Great event")
	check("genre", req.Genre, "festival")
	check("age_rating", req.AgeRating, "18+")
	check("teaser_url", req.TeaserURL, "https://cdn.example/teaser.mp4")
	check("trailer_url", req.TrailerURL, "https://cdn.example/trailer.mp4")
	check("meta_description", req.MetaDescription, "SEO description")
	check("meta_keywords", req.MetaKeywords, "festival,music")

	if !req.DurationMinutes.Present {
		t.Error("duration_minutes: Present=false, want true")
	}
	if req.DurationMinutes.Value == nil || *req.DurationMinutes.Value != 120 {
		t.Errorf("duration_minutes: got %v, want 120", req.DurationMinutes.Value)
	}
}

func TestUpdateEventRequest_AllMetadataFields_Null(t *testing.T) {
	body := `{
		"slug": null,
		"short_description": null,
		"genre": null,
		"age_rating": null,
		"duration_minutes": null,
		"teaser_url": null,
		"trailer_url": null,
		"meta_description": null,
		"meta_keywords": null
	}`
	var req updateEventRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	checkNull := func(name string, opt optionalString) {
		t.Helper()
		if !opt.Present {
			t.Errorf("%s: Present=false, want true (null was sent)", name)
		}
		if opt.Value != nil {
			t.Errorf("%s: Value=%v, want nil (null means clear)", name, opt.Value)
		}
	}
	checkNull("slug", req.Slug)
	checkNull("short_description", req.ShortDescription)
	checkNull("genre", req.Genre)
	checkNull("age_rating", req.AgeRating)
	checkNull("teaser_url", req.TeaserURL)
	checkNull("trailer_url", req.TrailerURL)
	checkNull("meta_description", req.MetaDescription)
	checkNull("meta_keywords", req.MetaKeywords)

	if !req.DurationMinutes.Present {
		t.Error("duration_minutes: Present=false, want true (null was sent)")
	}
	if req.DurationMinutes.Value != nil {
		t.Errorf("duration_minutes: Value=%v, want nil", req.DurationMinutes.Value)
	}
}

func TestUpdateEventRequest_AllMetadataFields_Absent(t *testing.T) {
	// An empty body must leave all metadata fields with Present=false.
	body := `{"name": "Just name change"}`
	var req updateEventRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	checkAbsent := func(name string, present bool) {
		t.Helper()
		if present {
			t.Errorf("%s: Present=true, want false (field absent from JSON)", name)
		}
	}
	checkAbsent("slug", req.Slug.Present)
	checkAbsent("short_description", req.ShortDescription.Present)
	checkAbsent("genre", req.Genre.Present)
	checkAbsent("age_rating", req.AgeRating.Present)
	checkAbsent("duration_minutes", req.DurationMinutes.Present)
	checkAbsent("teaser_url", req.TeaserURL.Present)
	checkAbsent("trailer_url", req.TrailerURL.Present)
	checkAbsent("meta_description", req.MetaDescription.Present)
	checkAbsent("meta_keywords", req.MetaKeywords.Present)
}

// ─────────────────────────────────────────────────────────────────────────────
// Metadata guard: absent fields must NOT trigger the metadata branch.
// ─────────────────────────────────────────────────────────────────────────────

func TestUpdateEventRequest_AbsentMetadata_GuardIsFalse(t *testing.T) {
	// When all metadata fields are absent, the guard expression in the handler
	// (req.Slug.Present || req.ShortDescription.Present || ...) must evaluate
	// to false so UpdateEventMetadata is never called.
	var req updateEventRequest
	if err := json.Unmarshal([]byte(`{"name":"Event Name"}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	metadataBranchWouldEnter := req.Slug.Present || req.ShortDescription.Present ||
		req.Genre.Present || req.AgeRating.Present || req.DurationMinutes.Present ||
		req.TeaserURL.Present || req.TrailerURL.Present ||
		req.MetaDescription.Present || req.MetaKeywords.Present
	if metadataBranchWouldEnter {
		t.Error("absent metadata fields: guard should be false, but at least one is Present=true")
	}
}

func TestUpdateEventRequest_NullMetadata_GuardIsTrue(t *testing.T) {
	// When any metadata field is set to null, the guard should be true.
	var req updateEventRequest
	if err := json.Unmarshal([]byte(`{"slug": null}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	metadataBranchWouldEnter := req.Slug.Present || req.ShortDescription.Present ||
		req.Genre.Present || req.AgeRating.Present || req.DurationMinutes.Present ||
		req.TeaserURL.Present || req.TrailerURL.Present ||
		req.MetaDescription.Present || req.MetaKeywords.Present
	if !metadataBranchWouldEnter {
		t.Error("null metadata field: guard should be true, but all Present=false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func ptr(s string) *string { return &s }

func ptrInt32(n int32) *int32 { return &n }
