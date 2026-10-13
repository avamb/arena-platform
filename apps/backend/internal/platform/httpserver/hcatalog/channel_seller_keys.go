package hcatalog

import "encoding/json"

// sellerDisplayKeys are the members of settings.hosted_page that only the
// seller-display route writes (PATCH .../channels/{channel_id}/seller-display,
// HP-4a): whether the sales page shows the seller's legal data, and the
// public contact buyers' messages go to. That route checks the owner, the
// required data and the e-mail; the generic channel routes take the whole
// settings object from the caller, so they keep the stored values instead.
var sellerDisplayKeys = []string{"show_seller", "public_contact"}

// keepSellerDisplayKeys returns next with the seller-display keys of
// settings.hosted_page replaced by the stored ones: a value the caller sent
// is dropped, a stored value is kept. stored may be nil (a new channel: the
// keys are simply dropped). next must be a JSON object (NormalizeChannelSettings);
// anything that does not decode is returned unchanged.
func keepSellerDisplayKeys(next, stored json.RawMessage) json.RawMessage {
	if len(next) == 0 {
		return next
	}
	var n map[string]json.RawMessage
	if err := json.Unmarshal(next, &n); err != nil {
		return next
	}
	hp := map[string]json.RawMessage{}
	if raw, ok := n["hosted_page"]; ok {
		if err := json.Unmarshal(raw, &hp); err != nil || hp == nil {
			hp = map[string]json.RawMessage{}
		}
	}
	changed := false
	for _, k := range sellerDisplayKeys {
		if _, ok := hp[k]; ok {
			delete(hp, k)
			changed = true
		}
	}
	var st struct {
		HostedPage map[string]json.RawMessage `json:"hosted_page"`
	}
	if len(stored) > 0 {
		_ = json.Unmarshal(stored, &st)
	}
	for _, k := range sellerDisplayKeys {
		if v, ok := st.HostedPage[k]; ok {
			hp[k] = v
			changed = true
		}
	}
	if !changed {
		return next
	}
	if _, had := n["hosted_page"]; !had && len(hp) == 0 {
		return next
	}
	b, err := json.Marshal(hp)
	if err != nil {
		return next
	}
	n["hosted_page"] = b
	out, err := json.Marshal(n)
	if err != nil {
		return next
	}
	return out
}
