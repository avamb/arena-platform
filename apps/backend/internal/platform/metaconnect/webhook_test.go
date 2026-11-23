package metaconnect

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

func hubSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookSignature(t *testing.T) {
	body := []byte(`{"object":"application","entry":[]}`)
	good := hubSignature(testSecret, body)
	if !VerifyWebhookSignature(body, good, testSecret) {
		t.Fatal("a good signature is refused")
	}
	upper := "sha256=" + strings.ToUpper(strings.TrimPrefix(good, "sha256="))
	if !VerifyWebhookSignature(body, upper, testSecret) {
		t.Fatal("an upper-case hex signature is refused")
	}
	for name, c := range map[string]struct {
		body   []byte
		header string
		secret string
	}{
		"missing":       {body, "", testSecret},
		"wrong secret":  {body, hubSignature("other", body), testSecret},
		"changed body":  {[]byte(`{"object":"application","entry":[{}]}`), good, testSecret},
		"no prefix":     {body, strings.TrimPrefix(good, "sha256="), testSecret},
		"sha1 prefix":   {body, "sha1=" + strings.TrimPrefix(good, "sha256="), testSecret},
		"not hex":       {body, "sha256=zz", testSecret},
		"short":         {body, good[:len(good)-2], testSecret},
		"empty secret":  {body, hubSignature("", body), ""},
		"prefix only":   {body, "sha256=", testSecret},
		"double prefix": {body, "sha256=" + good, testSecret},
	} {
		if VerifyWebhookSignature(c.body, c.header, c.secret) {
			t.Errorf("%s: verified", name)
		}
	}
}

func TestVerifyWebhookSubscription(t *testing.T) {
	if !VerifyWebhookSubscription("subscribe", "tok-1", "tok-1") {
		t.Fatal("a matching token is refused")
	}
	for name, c := range map[string][3]string{
		"wrong token":  {"subscribe", "tok-2", "tok-1"},
		"wrong mode":   {"unsubscribe", "tok-1", "tok-1"},
		"no mode":      {"", "tok-1", "tok-1"},
		"empty config": {"subscribe", "", ""},
		"prefix":       {"subscribe", "tok", "tok-1"},
	} {
		if VerifyWebhookSubscription(c[0], c[1], c[2]) {
			t.Errorf("%s: verified", name)
		}
	}
}

func TestUninstalledBusinesses(t *testing.T) {
	body := []byte(`{"object":"application","entry":[
	  {"id":"app-1","time":1700000000,"changes":[
	    {"field":"business_integration_uninstall","value":{"business_manager_id":"900","user_id":"1"}},
	    {"field":"permissions","value":{"business_manager_id":"777"}},
	    {"field":"business_integration_uninstall","value":{"business_id":901}},
	    {"field":"business_integration_uninstall","value":{"business_manager_id":"900"}},
	    {"field":"business_integration_uninstall","value":{"business_manager_id":"","business_id":"902"}},
	    {"field":"business_integration_uninstall","value":"not an object"},
	    {"field":"business_integration_uninstall","value":{"business_manager_id":1.5}},
	    {"field":"business_integration_uninstall","value":{}}
	  ]},
	  {"id":"app-1","changes":[{"field":"business_integration_uninstall","value":{"business_manager_id":" 903 "}}]}
	]}`)
	got, err := UninstalledBusinesses(body)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"900", "901", "902", "903"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("businesses = %v, want %v", got, want)
	}
	if got, err := UninstalledBusinesses([]byte(`{"object":"page","entry":[{"changes":[{"field":"feed","value":{"business_id":"1"}}]}]}`)); err != nil || len(got) != 0 {
		t.Fatalf("other field: %v %v", got, err)
	}
	if _, err := UninstalledBusinesses([]byte(`not json`)); err == nil {
		t.Fatal("not JSON parsed")
	}
}
