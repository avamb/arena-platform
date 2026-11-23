package hmetainsights

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func jsonFields(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

// The wire shapes carry exactly the fields of the generated OpenAPI types.
func TestWireShapesMatchOpenAPI(t *testing.T) {
	pairs := []struct {
		ours, spec any
	}{
		{Sync{}, openapi.MetaAdsSync{}},
		{Period{}, openapi.MetaAdsPeriod{}},
		{MetaTotals{}, openapi.MetaAdsMetaTotals{}},
		{OurTotals{}, openapi.MetaAdsOurTotals{}},
		{Untagged{}, openapi.MetaAdsUntagged{}},
		{EventRef{}, openapi.MetaAdsEventRef{}},
		{Campaign{}, openapi.MetaAdsCampaign{}},
		{EventReport{}, openapi.MetaAdsEventReport{}},
		{EventRow{}, openapi.MetaAdsEventRow{}},
		{APIVolume{}, openapi.MetaAdsApiVolume{}},
		{OrgSummary{}, openapi.MetaAdsOrgSummary{}},
		{RefreshResult{}, openapi.MetaAdsRefreshResult{}},
	}
	for _, p := range pairs {
		a, b := jsonFields(reflect.TypeOf(p.ours)), jsonFields(reflect.TypeOf(p.spec))
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%T fields %v, spec %T fields %v", p.ours, a, p.spec, b)
		}
	}
}

// MER is written as a JSON number, null when unknown.
func TestMERIsANumber(t *testing.T) {
	b, err := json.Marshal(EventRow{MER: optMER("2.50")})
	if err != nil || !strings.Contains(string(b), `"mer":2.50`) {
		t.Fatalf("%s %v", b, err)
	}
	b, _ = json.Marshal(EventRow{MER: optMER("")})
	if !strings.Contains(string(b), `"mer":null`) {
		t.Fatalf("%s", b)
	}
}

// Meta switched off: every route answers 503 meta.disabled before anything
// else.
func TestDisabledAnswers503(t *testing.T) {
	h := New(nil, false, nil)
	for _, fn := range []http.HandlerFunc{h.HandleEvent, h.HandleSummary, h.HandleRefresh} {
		rec := httptest.NewRecorder()
		fn(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "meta.disabled") {
			t.Errorf("got %d %s", rec.Code, rec.Body.String())
		}
	}
}
