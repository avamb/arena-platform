package hbil24

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/promoattempts"
)

type attemptsPromoQ struct{ row gen.PromoCodeRow }

func (q attemptsPromoQ) GetPromoCodeByCodeCI(context.Context, uuid.UUID, string) (gen.PromoCodeRow, error) {
	return q.row, nil
}
func (attemptsPromoQ) AppendGatewaySessionPromoCode(context.Context, uuid.UUID, []string) error {
	return nil
}
func (attemptsPromoQ) CountPromoCodeRedemptions(context.Context, uuid.UUID) (int32, error) {
	return 0, nil
}
func (attemptsPromoQ) CountPromoRedemptionsByCustomer(context.Context, uuid.UUID, uuid.UUID) (int32, error) {
	return 0, nil
}
func (attemptsPromoQ) CountPromoRedemptionsByBuyerEmail(context.Context, uuid.UUID, string) (int32, error) {
	return 0, nil
}

func TestEvaluatePromoCode_JournalsTheVerdictAndSurvivesAFailingJournal(t *testing.T) {
	orgID, sessionID, otherSession := uuid.New(), uuid.New(), uuid.New()
	row := gen.PromoCodeRow{
		ID: uuid.New(), OrgID: orgID, Code: "RED30", DiscountType: "percent", DiscountValue: 30,
		Status: "active", AppliesToSessionIDs: []string{otherSession.String()},
	}
	pc := promoCtx{
		cc:   cartCtx{orgID: orgID, gw: gen.GatewaySessionRow{ID: uuid.New()}},
		snap: cartSnapshot{currency: "EUR", lines: []cartLine{{price: 1000, sessionID: sessionID}}},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var mu sync.Mutex
	var got []promoattempts.Row
	for _, failing := range []bool{false, true} {
		got = nil
		rec := promoattempts.New(nil, "s", logger).WithSyncWrites().WithInsert(func(_ context.Context, rw promoattempts.Row) error {
			mu.Lock()
			got = append(got, rw)
			mu.Unlock()
			if failing {
				return errors.New("journal is down")
			}
			return nil
		})
		h := &Handler{promoQ: attemptsPromoQ{row: row}, promoRec: rec, logger: logger}

		v, err := h.evaluatePromoCode(context.Background(), pc, promoattempts.SurfaceGatewayAddPromoCodes, "red30", time.Now())
		if err != nil {
			t.Fatalf("failing=%v: evaluatePromoCode error %v; a journal failure must not reach the caller", failing, err)
		}
		if v.errCode != "promo.session_not_applicable" {
			t.Fatalf("failing=%v: verdict %q, want promo.session_not_applicable", failing, v.errCode)
		}
		if len(got) != 1 || got[0].Reason != promoattempts.ReasonWrongSession || got[0].Result != promoattempts.ResultRejected {
			t.Fatalf("failing=%v: journaled %+v, want one rejected/wrong_session row", failing, got)
		}
		if got[0].PromoCodeID != row.ID || got[0].Surface != promoattempts.SurfaceGatewayAddPromoCodes ||
			len(got[0].Sessions) != 1 || got[0].Sessions[0] != sessionID || got[0].GatewaySessionID != pc.cc.gw.ID {
			t.Fatalf("failing=%v: row lacks code, surface, session or gateway session: %+v", failing, got[0])
		}
	}

	// No journal wired: same verdict, nothing recorded.
	h := &Handler{promoQ: attemptsPromoQ{row: row}, logger: logger}
	if v, err := h.evaluatePromoCode(context.Background(), pc, promoattempts.SurfaceGatewayCheckKDP, "RED30", time.Now()); err != nil || v.errCode == "" {
		t.Fatalf("without a journal: %v, %q", err, v.errCode)
	}
}
