// payment_recover.go — a payment outcome arena read back from the provider
// itself, run through the webhook's own state machine (П-19).
//
// The checkout.payment_recover job finds a hosted payment attempt whose
// creation answer was lost and asks the provider about it. When the provider
// says the order is paid, that is exactly what its webhook would have said:
// the event is processed by processPaymentWebhook, the same code, the same
// transaction, the same (provider_payment_id, event_type) idempotency key —
// so a webhook arriving before or after the job changes nothing twice.
package hcheckout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

// ConfirmedPaymentSource names where a read-back payment event came from:
// the organization and the payment configuration whose credentials read it.
type ConfirmedPaymentSource struct {
	OrgID    uuid.UUID
	ConfigID uuid.UUID
	// Provider is the module's registry name ("allpay").
	Provider string
}

// ApplyProviderConfirmedPayment processes ev — normalized and confirmed with
// the provider exactly as the module's verified webhook would be — through
// the per-config webhook route's state machine, scoped to src the same way
// that route scopes a delivery (only a payment of this org and this provider
// can move). It answers the status and body that route would have answered;
// a status of 500 or above is an error, so the caller retries later.
func (h *Handler) ApplyProviderConfirmedPayment(ctx context.Context, src ConfirmedPaymentSource, ev payments.NormalizedEvent) (int, []byte, error) {
	if h == nil {
		return 0, nil, errors.New("hcheckout: no handler to apply a payment with")
	}
	body := []byte(ev.Raw)
	if len(body) == 0 || !json.Valid(body) {
		body, _ = json.Marshal(map[string]string{
			"source":              "provider_read_back",
			"provider_payment_id": ev.ProviderPaymentID,
		})
		ev.Raw = body
	}
	orgID, configID := src.OrgID, src.ConfigID
	route := webhookRoute{
		Kind:             observability.RouteKindConfig,
		ExpectedOrgID:    &orgID,
		ForeignEventIsOK: true,
		ExpectedProvider: src.Provider,
		ConfigID:         &configID,
		Event:            &ev,
		Provider:         src.Provider,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/internal/payment-recover", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	rec := &bufferedResponse{header: http.Header{}, status: http.StatusOK}
	h.processPaymentWebhook(rec, req, body, route)
	if rec.status >= http.StatusInternalServerError {
		return rec.status, rec.body.Bytes(), fmt.Errorf("hcheckout: applying the read-back payment %s answered %d: %s",
			ev.ProviderPaymentID, rec.status, rec.body.String())
	}
	return rec.status, rec.body.Bytes(), nil
}

// bufferedResponse is the http.ResponseWriter the read-back runs against.
type bufferedResponse struct {
	header      http.Header
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.wroteHeader {
		return
	}
	b.status, b.wroteHeader = status, true
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	b.wroteHeader = true
	return b.body.Write(p)
}
