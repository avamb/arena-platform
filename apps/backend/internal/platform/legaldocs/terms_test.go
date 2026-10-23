package legaldocs

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func strp(s string) *string { return &s }

// fakeStore keeps documents per locale, one seller, events by slug and the
// renderings it stored.
type fakeStore struct {
	docs       map[string]gen.LegalDocumentRow
	seller     *gen.LegalTermsSellerRow
	events     map[string]gen.LegalTermsEventRow
	renderings map[string]gen.LegalRenderingRow
	inserts    int
}

func newFakeStore(body string) *fakeStore {
	s := &fakeStore{
		docs:       map[string]gen.LegalDocumentRow{},
		events:     map[string]gen.LegalTermsEventRow{},
		renderings: map[string]gen.LegalRenderingRow{},
		seller: &gen.LegalTermsSellerRow{
			OrgID: uuid.New(), OrgSlug: "teatro", OrgName: "Teatro", LegalName: strp("Teatro Madrid S.L."),
			TaxID: strp("ESB12345678"), TaxIDScheme: strp("eu_vat"), AddressLine1: strp("Calle Mayor 1"),
			AddressCity: strp("Madrid"), AddressPostalCode: strp("28001"), AddressCountry: strp("es"),
			OrgCountry: "ES", ChannelID: uuid.New(), PaymentMode: "direct_merchant",
			PublicContact: []byte(`{"email":"tickets@teatro.example","phone":"+34 600 111 222"}`),
		},
	}
	for _, l := range []string{"en", "es"} {
		s.docs[l] = gen.LegalDocumentRow{ID: uuid.New(), Kind: KindSaleTerms, Locale: l, Version: 3, Status: StatusPublished, BodyTemplate: body}
	}
	return s
}

func (s *fakeStore) GetPublishedLegalDocument(_ context.Context, _, locale string) (gen.LegalDocumentRow, error) {
	d, ok := s.docs[locale]
	if !ok {
		return gen.LegalDocumentRow{}, pgx.ErrNoRows
	}
	return d, nil
}

func (s *fakeStore) GetLegalTermsSeller(_ context.Context, orgID uuid.UUID, _ string) (gen.LegalTermsSellerRow, error) {
	if s.seller == nil || s.seller.OrgID != orgID {
		return gen.LegalTermsSellerRow{}, pgx.ErrNoRows
	}
	return *s.seller, nil
}

func (s *fakeStore) GetLegalTermsEventBySlug(_ context.Context, _ uuid.UUID, slug string, promoterID *uuid.UUID) (gen.LegalTermsEventRow, error) {
	e, ok := s.events[slug]
	if !ok || (promoterID != nil && (e.PromoterID == nil || *e.PromoterID != *promoterID)) {
		return gen.LegalTermsEventRow{}, pgx.ErrNoRows
	}
	return e, nil
}

func (s *fakeStore) GetLegalTermsEventByID(_ context.Context, _, id uuid.UUID) (gen.LegalTermsEventRow, error) {
	for _, e := range s.events {
		if e.EventID == id {
			return e, nil
		}
	}
	return gen.LegalTermsEventRow{}, pgx.ErrNoRows
}

func (s *fakeStore) GetLegalTermsPublishedEventByID(ctx context.Context, org, id uuid.UUID, promoterID *uuid.UUID) (gen.LegalTermsEventRow, error) {
	e, err := s.GetLegalTermsEventByID(ctx, org, id)
	if err != nil || (promoterID != nil && (e.PromoterID == nil || *e.PromoterID != *promoterID)) {
		return gen.LegalTermsEventRow{}, pgx.ErrNoRows
	}
	return e, nil
}

func (s *fakeStore) GetLegalTermsPromoter(context.Context, uuid.UUID, uuid.UUID) (gen.LegalTermsPromoterRow, error) {
	return gen.LegalTermsPromoterRow{}, pgx.ErrNoRows
}

func (s *fakeStore) GetLegalRenderingByInputs(_ context.Context, doc uuid.UUID, hash string) (gen.LegalRenderingRow, error) {
	r, ok := s.renderings[doc.String()+hash]
	if !ok {
		return gen.LegalRenderingRow{}, pgx.ErrNoRows
	}
	return r, nil
}

func (s *fakeStore) InsertLegalRendering(_ context.Context, p gen.InsertLegalRenderingParams) (gen.LegalRenderingRow, error) {
	s.inserts++
	r := gen.LegalRenderingRow{ID: uuid.New(), OrgID: p.OrgID, ChannelID: p.ChannelID, EventID: p.EventID,
		DocumentID: p.DocumentID, DocumentVersion: p.DocumentVersion, Locale: p.Locale, RefundMode: p.RefundMode,
		Inputs: p.Inputs, InputsHash: p.InputsHash, BodyHTML: p.BodyHTML, BodySHA256: p.BodySHA256}
	s.renderings[p.DocumentID.String()+p.InputsHash] = r
	return r, nil
}

const testBody = "## Terms {{document.version}}\n\nSeller: {{seller.legal_name}}. {{seller.ids_line}}. {{seller.address}}. {{seller.phone}}\n\n{{#if promoter.name}}Presented by {{promoter.name}}.{{/if}}\n\n{{refund.mode_text}}\n\n{{#if law.no_withdrawal}}No withdrawal.{{/if}}"

func TestRenderTerms_SubstitutesTheSellerAndStoresOnce(t *testing.T) {
	s := newFakeStore(testBody)
	req := TermsRequest{OrgID: s.seller.OrgID, Locale: "es-ES"}
	r, err := RenderTerms(context.Background(), s, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<h2>Terms 3</h2>", "Teatro Madrid S.L.", "ID de IVA ESB12345678", "Calle Mayor 1, 28001 Madrid, España", "+34 600 111 222", "No withdrawal.", "the law of España"} {
		if !strings.Contains(r.HTML, want) {
			t.Errorf("missing %q in %s", want, r.HTML)
		}
	}
	if strings.Contains(r.HTML, "tickets@teatro.example") {
		t.Error("the public e-mail is published")
	}
	if r.Locale != "es" || r.DocumentVersion != 3 || r.RefundMode != RefundByLaw || r.SHA256 != SHA256Hex([]byte(r.HTML)) {
		t.Errorf("rendering = %+v", r)
	}
	if got := r.TermsVersion(); got != "sale_terms:v3:es;seller:"+s.seller.OrgID.String() {
		t.Errorf("terms version = %q", got)
	}
	again, err := RenderTerms(context.Background(), s, req)
	if err != nil || again.ID != r.ID || s.inserts != 1 {
		t.Fatalf("a second render stored again: %v %v inserts=%d", err, again.ID == r.ID, s.inserts)
	}
}

func TestRenderTerms_LocaleFallsBackToEnglish(t *testing.T) {
	s := newFakeStore(testBody)
	for _, l := range []string{"fr", "xx", ""} {
		r, err := RenderTerms(context.Background(), s, TermsRequest{OrgID: s.seller.OrgID, Locale: l})
		if err != nil || r.Locale != "en" {
			t.Fatalf("locale %q: %v %q", l, err, r.Locale)
		}
	}
}

func TestRenderTerms_EventOverridesTheOrganizationRefundMode(t *testing.T) {
	s := newFakeStore(testBody)
	s.seller.RefundPolicyMode = strp(RefundUntilSalesEnd)
	r, err := RenderTerms(context.Background(), s, TermsRequest{OrgID: s.seller.OrgID})
	if err != nil || r.RefundMode != RefundUntilSalesEnd {
		t.Fatalf("org mode: %v %q", err, r.RefundMode)
	}
	pid := uuid.New()
	s.events["show"] = gen.LegalTermsEventRow{EventID: uuid.New(), RefundPolicyMode: strp(RefundNone), PromoterID: &pid, PromoterName: strp("Kolybel")}
	r, err = RenderTerms(context.Background(), s, TermsRequest{OrgID: s.seller.OrgID, EventSlug: "show"})
	if err != nil || r.RefundMode != RefundNone || !strings.Contains(r.HTML, "Presented by Kolybel.") ||
		!strings.Contains(r.HTML, "You cannot return a ticket") {
		t.Fatalf("event: %v %q %s", err, r.RefundMode, r.HTML)
	}
	other := uuid.New()
	if _, err := RenderTerms(context.Background(), s, TermsRequest{OrgID: s.seller.OrgID, EventSlug: "show", PromoterID: &other}); err != ErrNotFound {
		t.Fatalf("event of another promoter: %v", err)
	}
	if _, err := RenderTerms(context.Background(), s, TermsRequest{OrgID: s.seller.OrgID, EventSlug: "nope"}); err != ErrNotFound {
		t.Fatalf("unknown event: %v", err)
	}
}

func TestRenderTerms_MerchantOfRecordNamesThePlatform(t *testing.T) {
	s := newFakeStore(testBody)
	s.seller.PaymentMode = "merchant_of_record"
	r, err := RenderTerms(context.Background(), s, TermsRequest{OrgID: s.seller.OrgID})
	if err != nil || r.SellerLegalName != PlatformSellerName || strings.Contains(r.HTML, "Teatro Madrid") {
		t.Fatalf("MoR: %v %q %s", err, r.SellerLegalName, r.HTML)
	}
}

// TestRenderTerms_IncompleteSellerIsRefused: terms are never rendered from
// partial seller data (the same rule as the seller block, sellerinfo.Missing);
// a merchant-of-record channel names the platform and is not affected.
func TestRenderTerms_IncompleteSellerIsRefused(t *testing.T) {
	for name, edit := range map[string]func(*fakeStore){
		"no tax number":   func(s *fakeStore) { s.seller.TaxID = nil },
		"no street":       func(s *fakeStore) { s.seller.AddressLine1 = strp(" ") },
		"no public email": func(s *fakeStore) { s.seller.PublicContact = []byte(`{"phone":"+34 600"}`) },
	} {
		s := newFakeStore(testBody)
		edit(s)
		if _, err := RenderTerms(context.Background(), s, TermsRequest{OrgID: s.seller.OrgID}); err != ErrSellerIncomplete {
			t.Errorf("%s: %v", name, err)
		}
		if len(s.renderings) != 0 {
			t.Errorf("%s: a rendering was stored", name)
		}
		s.seller.PaymentMode = "merchant_of_record"
		if _, err := RenderTerms(context.Background(), s, TermsRequest{OrgID: s.seller.OrgID}); err != nil {
			t.Errorf("%s under MoR: %v", name, err)
		}
	}
}

func TestRenderTerms_NotFoundAndNoDocument(t *testing.T) {
	s := newFakeStore(testBody)
	if _, err := RenderTerms(context.Background(), s, TermsRequest{OrgID: uuid.New()}); err != ErrNotFound {
		t.Fatalf("unknown org: %v", err)
	}
	s.docs = map[string]gen.LegalDocumentRow{}
	if _, err := RenderTerms(context.Background(), s, TermsRequest{OrgID: s.seller.OrgID}); err != ErrNoDocument {
		t.Fatalf("no document: %v", err)
	}
}

func TestRenderTerms_ChangedDataIsANewRendering(t *testing.T) {
	s := newFakeStore(testBody)
	a, _ := RenderTerms(context.Background(), s, TermsRequest{OrgID: s.seller.OrgID})
	s.seller.LegalName = strp("Teatro Nuevo S.L.")
	b, _ := RenderTerms(context.Background(), s, TermsRequest{OrgID: s.seller.OrgID})
	if a.ID == b.ID || s.inserts != 2 {
		t.Fatalf("a changed seller kept the old rendering: %v", a.ID == b.ID)
	}
}

func TestInputs_HashIsStable(t *testing.T) {
	org, ch := uuid.MustParse("11111111-1111-1111-1111-111111111111"), uuid.MustParse("22222222-2222-2222-2222-222222222222")
	v1 := Values{"seller.legal_name": "A", "refund.no_refund": true, "law.country": "Spain"}
	v2 := Values{"law.country": "Spain", "refund.no_refund": true, "seller.legal_name": "A"}
	raw1, h1, err := Inputs(Context{OrgID: org, ChannelID: &ch}, v1)
	if err != nil {
		t.Fatal(err)
	}
	_, h2, _ := Inputs(Context{OrgID: org, ChannelID: &ch}, v2)
	if h1 != h2 || h1 != SHA256Hex(raw1) || len(h1) != 64 {
		t.Fatalf("hash not stable: %s %s", h1, h2)
	}
	// Pinned: changing the canonical form must be a conscious RendererVersion bump.
	const want = `{"renderer":1,"org_id":"11111111-1111-1111-1111-111111111111","channel_id":"22222222-2222-2222-2222-222222222222","event_id":null,"values":{"law.country":"Spain","refund.no_refund":true,"seller.legal_name":"A"}}`
	if string(raw1) != want {
		t.Fatalf("canonical inputs changed:\n%s\n%s", raw1, want)
	}
	_, h3, _ := Inputs(Context{OrgID: org}, v1)
	if h3 == h1 {
		t.Fatal("the channel is not part of the hash")
	}
}

func TestResolveRefundMode(t *testing.T) {
	bad := "weird"
	cases := []struct {
		event, org *string
		want       string
	}{
		{nil, nil, RefundByLaw},
		{nil, strp(RefundNone), RefundNone},
		{strp(RefundUntilSalesEnd), strp(RefundNone), RefundUntilSalesEnd},
		{&bad, strp(RefundNone), RefundNone},
	}
	for _, c := range cases {
		if got := ResolveRefundMode(c.event, c.org); got != c.want {
			t.Errorf("ResolveRefundMode = %q, want %q", got, c.want)
		}
	}
}

func TestNormalizeLocale(t *testing.T) {
	for in, want := range map[string]string{"cs-CZ": "cs", "HE": "he", "fr_FR": "fr", "it": "en", "": "en"} {
		if got := NormalizeLocale(in); got != want {
			t.Errorf("NormalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCountryName(t *testing.T) {
	if got := CountryName("cz", "en"); got != "Czechia" && got != "Czech Republic" {
		t.Errorf("CZ en = %q", got)
	}
	if got := CountryName("ES", "es"); got != "España" {
		t.Errorf("ES es = %q", got)
	}
	if got := CountryName("", "en"); got != "" {
		t.Errorf("empty = %q", got)
	}
}
