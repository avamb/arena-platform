//go:build integration

package gen

import (
	"context"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/tests/pgtest"
)

func TestOrganizationLegalFieldsRoundTrip(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()
	q := New(pool)
	ctx := context.Background()

	created, err := q.InsertOrganization(ctx, "Legal roundtrip", "legal-roundtrip", "DE", "en", 1200)
	if err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	legalName, taxID, scheme := "TEST_392 Arena GmbH", "DE123456789", "eu_vat"
	registration, line1, line2 := "HRB 123", "Test Strasse 1", "Suite 392"
	postal, city, country := "10115", "Berlin", "DE"
	email, phone, website := "legal392@example.test", "+4930123456", "https://example.test"
	_, err = q.UpdateOrganization(ctx, created.ID, "", "", "", "", 0,
		&legalName, &taxID, &scheme, &registration, &line1, &line2, &postal, &city, &country, &email, &phone, &website, "pending", nil)
	if err != nil {
		t.Fatalf("update organization legal fields: %v", err)
	}
	got, err := q.GetOrganizationByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("get organization: %v", err)
	}
	if got.LegalName == nil || *got.LegalName != legalName || got.TaxID == nil || *got.TaxID != taxID || got.ContactEmail == nil || *got.ContactEmail != email || got.KybStatus != "pending" {
		t.Fatalf("legal field roundtrip failed: %+v", got)
	}
}
