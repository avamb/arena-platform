package hcheckout

import "testing"

// A prepared widget order (spec 65) may reach payment without its buyer: the
// completed session's collected e-mail must come out of the envelope.
func TestParseWebhook_BuyerEmailFromCheckoutSession(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			"customer_details wins",
			`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_1","payment_status":"paid",
			  "customer_email":"created@example.com","customer_details":{"email":" paid@example.com "}}}}`,
			"paid@example.com",
		},
		{
			"customer_email when no details",
			`{"id":"evt_2","type":"checkout.session.completed","data":{"object":{"id":"cs_2","payment_status":"paid",
			  "customer_email":"created@example.com","customer_details":null}}}`,
			"created@example.com",
		},
		{
			"empty details email falls back",
			`{"id":"evt_3","type":"checkout.session.completed","data":{"object":{"id":"cs_3","payment_status":"paid",
			  "customer_email":"created@example.com","customer_details":{"email":""}}}}`,
			"created@example.com",
		},
		{
			"none",
			`{"id":"evt_4","type":"checkout.session.completed","data":{"object":{"id":"cs_4","payment_status":"paid"}}}`,
			"",
		},
	}
	for _, tc := range cases {
		req, err := parseWebhookPaymentIntentRequest([]byte(tc.body))
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		if req.BuyerEmail != tc.want {
			t.Errorf("%s: BuyerEmail = %q, want %q", tc.name, req.BuyerEmail, tc.want)
		}
	}
}

// The flat mock shape carries no address.
func TestParseWebhook_FlatShapeHasNoBuyerEmail(t *testing.T) {
	req, err := parseWebhookPaymentIntentRequest([]byte(`{"provider_payment_id":"pi_1","event_type":"mock.succeeded","buyer_email":"x@example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.BuyerEmail != "" {
		t.Fatalf("flat shape BuyerEmail = %q, want empty", req.BuyerEmail)
	}
}
