package hcatalog

import "encoding/json"

// sellerDisplayKeys are the members of settings.hosted_page that only the
// seller-display route writes (PATCH .../channels/{channel_id}/seller-display,
// HP-4a): whether the sales page shows the seller's legal data, and the
// public contact buyers' messages go to, and the operator's seller_hidden
// switch (platform superadmin only), and the buyer consent at checkout
// (consent_required, P2: switched on only while the seller profile is
// complete). That route checks the owner, the required data and the e-mail;
// the generic channel routes take the whole settings object from the caller,
// so they keep the stored values instead.
var sellerDisplayKeys = []string{"show_seller", "public_contact", "seller_hidden", "consent_required"}

// siteDisplayKeys are the members of settings.site that the same route writes:
// thank_you_posters switches the posters of the "You may also like" block of
// the thank-you page.
var siteDisplayKeys = []string{"thank_you_posters"}

// keepSellerDisplayKeys returns next with the seller-display keys of
// settings.hosted_page (and settings.site.thank_you_posters) replaced by the
// stored ones: a value the caller sent is dropped, a stored value is kept.
// stored may be nil (a new channel: the keys are simply dropped). next must be
// a JSON object (NormalizeChannelSettings); anything that does not decode is
// returned unchanged.
func keepSellerDisplayKeys(next, stored json.RawMessage) json.RawMessage {
	if len(next) == 0 {
		return next
	}
	var n map[string]json.RawMessage
	if err := json.Unmarshal(next, &n); err != nil {
		return next
	}
	var st map[string]map[string]json.RawMessage
	if len(stored) > 0 {
		_ = json.Unmarshal(stored, &st)
	}
	c1 := keepSection(n, st, "hosted_page", sellerDisplayKeys)
	c2 := keepSection(n, st, "site", siteDisplayKeys)
	if !c1 && !c2 {
		return next
	}
	out, err := json.Marshal(n)
	if err != nil {
		return next
	}
	return out
}

// keepSection rewrites n[section] so that the given keys carry the stored
// values (or are absent). It reports whether it changed n.
func keepSection(n map[string]json.RawMessage, st map[string]map[string]json.RawMessage, section string, keys []string) bool {
	sec := map[string]json.RawMessage{}
	if raw, ok := n[section]; ok {
		if err := json.Unmarshal(raw, &sec); err != nil || sec == nil {
			sec = map[string]json.RawMessage{}
		}
	}
	changed := false
	for _, k := range keys {
		if _, ok := sec[k]; ok {
			delete(sec, k)
			changed = true
		}
	}
	for _, k := range keys {
		if v, ok := st[section][k]; ok {
			sec[k] = v
			changed = true
		}
	}
	if !changed {
		return false
	}
	if _, had := n[section]; !had && len(sec) == 0 {
		return false
	}
	b, err := json.Marshal(sec)
	if err != nil {
		return false
	}
	n[section] = b
	return true
}
