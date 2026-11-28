// import_external_sale.go: actionEvent.externalSale of the arena event
// bundle (migration 0221, spec 63 §14.4). Absent keeps the session's sales
// mode, null puts it back on sale on the platform, an object hands it to a
// partner box office. The rules (address, name, price, lock) are extsale's,
// the same ones the organizer API applies.
package himports

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/extsale"
)

// externalSaleFields maps extsale's field names onto the bundle's JSON pointers.
var externalSaleFields = map[string]string{
	"external.url":         "/actionEvent/externalSale/url",
	"external.seller_name": "/actionEvent/externalSale/sellerName",
	"external.price_from":  "/actionEvent/externalSale/priceFrom",
	"external.currency":    "/actionEvent/externalSale/currency",
}

// importExternalSale reads and checks actionEvent.externalSale. The checked
// block is only meaningful for bil24compat.ExternalSaleObject.
func importExternalSale(e bil24compat.ImportSessionActionEvent) (bil24compat.ExternalSaleKind, extsale.Sale, error) {
	kind, raw, err := e.ParseExternalSale()
	if err != nil {
		return kind, extsale.Sale{}, failImportAt(http.StatusUnprocessableEntity, "import.invalid_external_sale",
			"/actionEvent/externalSale", err.Error())
	}
	if kind != bil24compat.ExternalSaleObject {
		return kind, extsale.Sale{}, nil
	}
	sale, ferr := extsale.Normalize(extsale.Input{
		URL: raw.URL, SellerName: raw.SellerName, PriceFrom: raw.PriceFrom, Currency: raw.Currency,
	})
	if ferr != nil {
		return kind, extsale.Sale{}, failImportAt(http.StatusUnprocessableEntity, "import.invalid_external_sale",
			externalSaleFields[ferr.Field], ferr.Message)
	}
	return kind, sale, nil
}

// codeEventControlOnly is the 409 code of an event that only controls the
// entrance (the same word hcatalog answers with).
const codeEventControlOnly = "event.control_only"

// errControlOnlySession refuses a bundle that addresses a session which only
// controls the entrance (sales_mode control_only, migration 0228).
func errControlOnlySession() error {
	return failImportAt(http.StatusConflict, extsale.CodeSalesModeLocked, "/actionEvent/arenaSessionId",
		"the session cannot be changed by an import")
}

// errControlOnlyEvent refuses a bundle that addresses an event whose sessions
// only control the entrance.
func errControlOnlyEvent() error {
	return failImportAt(http.StatusConflict, codeEventControlOnly, "/action/arenaEventId",
		"the event cannot be changed by an import")
}

// requireCategoriesUnlessExternal finishes the categoryList check the pure
// validation postponed (importPlan.CategoriesDeferred): a bundle with no
// categories and no externalSale is only valid for an existing session a
// partner already sells, which keeps its mode. Any other session answers the
// same 422 import.categories_required the validation gives.
func requireCategoriesUnlessExternal(ctx context.Context, q *gen.Queries, plan importPlan, m arenaMatch) error {
	if !plan.CategoriesDeferred {
		return nil
	}
	if m.SessionID != uuid.Nil {
		cur, err := q.GetSessionByID(ctx, m.SessionID, m.Ctx.EventID)
		if err != nil {
			return fmt.Errorf("read session sales mode: %w", err)
		}
		if cur.SalesMode == gen.SessionSalesModeExternal {
			return nil
		}
	}
	return failImportAt(http.StatusUnprocessableEntity, "import.categories_required", "/categoryList",
		"categoryList must contain at least one category")
}

// applyArenaExternalSale writes the bundle's sales mode onto the session
// (inside the import's transaction, after the session row exists and, for an
// existing one, is locked) and reports whether the session is now sold by a
// partner. A partner-sold session owns no categories, so a bundle that still
// carries a categoryList for one is refused, and the caller skips the whole
// category pipeline.
func applyArenaExternalSale(ctx context.Context, q *gen.Queries, plan importPlan, sessionID, eventID uuid.UUID, seated *arenaSeatedPlan) (bool, error) {
	kind, sale, err := importExternalSale(plan.Request.ActionEvent)
	if err != nil {
		return false, err
	}
	cur, err := q.GetSessionByID(ctx, sessionID, eventID)
	if err != nil {
		return false, fmt.Errorf("read session sales mode: %w", err)
	}
	target := cur.SalesMode
	switch kind {
	case bil24compat.ExternalSaleNull:
		target = gen.SessionSalesModePlatform
	case bil24compat.ExternalSaleObject:
		target = gen.SessionSalesModeExternal
	}
	if target == gen.SessionSalesModeExternal {
		if seated != nil {
			return false, failImportAt(http.StatusConflict, extsale.CodeSalesModeLocked, "/actionEvent/externalSale",
				"a session with a seating plan cannot be sold by a partner box office")
		}
		if len(plan.Request.CategoryList) > 0 {
			return false, failImportAt(http.StatusConflict, extsale.CodeSoldExternally, "/categoryList",
				"a session sold by a partner box office has no categories here: send no categoryList")
		}
	}
	if target == cur.SalesMode && kind != bil24compat.ExternalSaleObject {
		return target == gen.SessionSalesModeExternal, nil
	}
	facts, err := q.GetSessionSalesModeLockFacts(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("read session sales mode facts: %w", err)
	}
	if err := extsale.CheckSwitch(cur.SalesMode, target, facts); err != nil {
		if errors.Is(err, extsale.ErrSalesModeLocked) {
			return false, failImportAt(http.StatusConflict, extsale.CodeSalesModeLocked, "/actionEvent/externalSale",
				"the session has orders, holds, seats or categories, so its sales mode cannot change")
		}
		return false, err
	}
	var url, name, currency *string
	var minor *int64
	if target == gen.SessionSalesModeExternal {
		url, name, minor, currency = &sale.URL, &sale.SellerName, sale.PriceFromMinor, sale.Currency
	}
	if _, err := q.SetSessionSalesMode(ctx, sessionID, eventID, target, url, name, minor, currency); err != nil {
		return false, fmt.Errorf("set session sales mode: %w", err)
	}
	return target == gen.SessionSalesModeExternal, nil
}
