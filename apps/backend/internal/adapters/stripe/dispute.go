package stripe

// dispute.go — Stripe disputes (spec 51.3, PAY-16): the charge.dispute.*
// events and the evidence API.
//
//   - GET  /v1/disputes/{id}        — read the dispute back
//   - GET  /v1/charges/{id}         — the payment behind it, 3-D Secure
//   - POST files.stripe.com/v1/files (purpose=dispute_evidence)
//   - POST /v1/disputes/{id}        — evidence[...], submit=false|true
//   - POST /v1/disputes/{id}/close  — accept the dispute
//
// submit=true is final: Stripe takes one answer per dispute. Arena calls it
// only after a person pressed "Submit".

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

var _ payments.DisputeResponder = (*Adapter)(nil)

// defaultFilesBaseURL is Stripe's file upload host. A test BaseURL (a stub)
// serves uploads too.
const defaultFilesBaseURL = "https://files.stripe.com/v1"

func (a *Adapter) filesBaseURL() string {
	if a.cfg.BaseURL == defaultBaseURL {
		return defaultFilesBaseURL
	}
	return a.cfg.BaseURL
}

// disputeObject is the subset of Stripe's Dispute object arena reads.
type disputeObject struct {
	ID                 string `json:"id"`
	Object             string `json:"object"`
	Amount             int64  `json:"amount"`
	Currency           string `json:"currency"`
	Charge             string `json:"charge"`
	PaymentIntent      string `json:"payment_intent"`
	Reason             string `json:"reason"`
	Status             string `json:"status"`
	IsChargeRefundable bool   `json:"is_charge_refundable"`
	Created            int64  `json:"created"`
	EvidenceDetails    struct {
		DueBy           *int64 `json:"due_by"`
		HasEvidence     bool   `json:"has_evidence"`
		SubmissionCount int    `json:"submission_count"`
	} `json:"evidence_details"`
}

func (o disputeObject) toDispute() payments.Dispute {
	d := payments.Dispute{
		ID:                 strings.TrimSpace(o.ID),
		Status:             o.Status,
		Reason:             o.Reason,
		AmountMinor:        o.Amount,
		Currency:           strings.ToUpper(o.Currency),
		ChargeID:           strings.TrimSpace(o.Charge),
		PaymentIntentID:    strings.TrimSpace(o.PaymentIntent),
		IsChargeRefundable: o.IsChargeRefundable,
		HasEvidence:        o.EvidenceDetails.HasEvidence,
		SubmissionCount:    o.EvidenceDetails.SubmissionCount,
	}
	if o.EvidenceDetails.DueBy != nil && *o.EvidenceDetails.DueBy > 0 {
		t := time.Unix(*o.EvidenceDetails.DueBy, 0).UTC()
		d.DueBy = &t
	}
	if o.Created > 0 {
		d.Created = time.Unix(o.Created, 0).UTC()
	}
	return d
}

// disputeEventTypes are the dispute events arena handles.
var disputeEventTypes = map[string]payments.EventKind{
	"charge.dispute.created":          payments.EventDisputeOpened,
	"charge.dispute.updated":          payments.EventDisputeUpdated,
	"charge.dispute.closed":           payments.EventDisputeUpdated,
	"charge.dispute.funds_withdrawn":  payments.EventDisputeUpdated,
	"charge.dispute.funds_reinstated": payments.EventDisputeUpdated,
}

// normalizeDisputeEvent fills ev for a charge.dispute.* envelope. The
// payment is named by the dispute's payment_intent (pi_…), which arena
// stored as payment_intents.provider_charge_ref.
func normalizeDisputeEvent(ev *payments.NormalizedEvent, kind payments.EventKind, body []byte) {
	var env struct {
		Data struct {
			Object disputeObject `json:"object"`
		} `json:"data"`
	}
	ev.Kind = kind
	if err := json.Unmarshal(body, &env); err != nil {
		return
	}
	d := env.Data.Object.toDispute()
	switch ev.Type {
	case "charge.dispute.funds_withdrawn":
		d.FundsWithdrawn = true
	case "charge.dispute.funds_reinstated":
		d.FundsReinstated = true
	}
	ev.Dispute = &d
	ev.ProviderPaymentID = d.PaymentIntentID
	ev.ProviderChargeRef = d.PaymentIntentID
	ev.Status = d.Status
	ev.AmountMinor = d.AmountMinor
	ev.Currency = d.Currency
}

// GetDispute implements payments.DisputeResponder.
func (a *Adapter) GetDispute(ctx context.Context, disputeID string) (payments.Dispute, error) {
	if strings.TrimSpace(disputeID) == "" {
		return payments.Dispute{}, errors.New("stripe: dispute id is required")
	}
	body, _, err := a.doRequest(ctx, http.MethodGet, a.cfg.BaseURL+"/disputes/"+url.PathEscape(disputeID), nil, "")
	if err != nil {
		return payments.Dispute{}, disputeError(err)
	}
	var o disputeObject
	if err := json.Unmarshal(body, &o); err != nil {
		return payments.Dispute{}, fmt.Errorf("stripe: decode dispute: %w", err)
	}
	return o.toDispute(), nil
}

// ChargeFacts implements payments.DisputeResponder.
func (a *Adapter) ChargeFacts(ctx context.Context, chargeID string) (payments.ChargeFacts, error) {
	if strings.TrimSpace(chargeID) == "" {
		return payments.ChargeFacts{}, errors.New("stripe: charge id is required")
	}
	body, _, err := a.doRequest(ctx, http.MethodGet, a.cfg.BaseURL+"/charges/"+url.PathEscape(chargeID), nil, "")
	if err != nil {
		return payments.ChargeFacts{}, err
	}
	var ch struct {
		PaymentIntent                 string `json:"payment_intent"`
		CalculatedStatementDescriptor string `json:"calculated_statement_descriptor"`
		PaymentMethodDetails          struct {
			Card *struct {
				Brand        string `json:"brand"`
				Last4        string `json:"last4"`
				ThreeDSecure *struct {
					Result string `json:"result"`
				} `json:"three_d_secure"`
			} `json:"card"`
		} `json:"payment_method_details"`
	}
	if err := json.Unmarshal(body, &ch); err != nil {
		return payments.ChargeFacts{}, fmt.Errorf("stripe: decode charge: %w", err)
	}
	f := payments.ChargeFacts{PaymentIntentID: ch.PaymentIntent, StatementDescriptor: ch.CalculatedStatementDescriptor}
	if c := ch.PaymentMethodDetails.Card; c != nil {
		f.CardBrand, f.Last4 = c.Brand, c.Last4
		if c.ThreeDSecure != nil {
			f.ThreeDSecure = c.ThreeDSecure.Result
		}
	}
	return f, nil
}

// UploadDisputeFile implements payments.DisputeResponder: one multipart POST
// to the files host.
func (a *Adapter) UploadDisputeFile(ctx context.Context, f payments.DisputeFile, idempotencyKey string) (string, error) {
	if len(f.Content) == 0 {
		return "", errors.New("stripe: evidence file is empty")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("purpose", "dispute_evidence"); err != nil {
		return "", err
	}
	part, err := mw.CreateFormFile("file", f.Filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(f.Content); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.filesBaseURL()+"/files", &buf)
	if err != nil {
		return "", fmt.Errorf("stripe: build upload: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.SecretKey)
	req.Header.Set("Stripe-Version", stripeAPIVersion)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("stripe: http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("stripe: read upload response: %w", err)
	}
	if resp.StatusCode >= 400 {
		e := &apiError{Status: resp.StatusCode}
		var apiErr stripeAPIError
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Message != "" {
			e.Type, e.Code, e.Message = apiErr.Error.Type, apiErr.Error.Code, apiErr.Error.Message
		} else {
			e.raw = string(raw)
		}
		return "", disputeError(e)
	}
	var file struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &file); err != nil || file.ID == "" {
		return "", fmt.Errorf("stripe: decode uploaded file: %v", err)
	}
	return file.ID, nil
}

// UpdateDisputeEvidence implements payments.DisputeResponder.
func (a *Adapter) UpdateDisputeEvidence(ctx context.Context, disputeID string, evidence map[string]string, submit bool, idempotencyKey string) (payments.Dispute, error) {
	form := url.Values{}
	keys := make([]string, 0, len(evidence))
	for k := range evidence {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := evidence[k]; v != "" {
			form.Set("evidence["+k+"]", v)
		}
	}
	if submit {
		form.Set("submit", "true")
	} else {
		form.Set("submit", "false")
	}
	body, _, err := a.doRequest(ctx, http.MethodPost, a.cfg.BaseURL+"/disputes/"+url.PathEscape(disputeID), form, idempotencyKey)
	if err != nil {
		return payments.Dispute{}, disputeError(err)
	}
	var o disputeObject
	if err := json.Unmarshal(body, &o); err != nil {
		return payments.Dispute{}, fmt.Errorf("stripe: decode dispute: %w", err)
	}
	return o.toDispute(), nil
}

// CloseDispute implements payments.DisputeResponder.
func (a *Adapter) CloseDispute(ctx context.Context, disputeID, idempotencyKey string) (payments.Dispute, error) {
	body, _, err := a.doRequest(ctx, http.MethodPost, a.cfg.BaseURL+"/disputes/"+url.PathEscape(disputeID)+"/close", url.Values{}, idempotencyKey)
	if err != nil {
		return payments.Dispute{}, disputeError(err)
	}
	var o disputeObject
	if err := json.Unmarshal(body, &o); err != nil {
		return payments.Dispute{}, fmt.Errorf("stripe: decode dispute: %w", err)
	}
	return o.toDispute(), nil
}

// disputeError maps Stripe's refusals onto the provider-neutral errors: a
// key without the right is ErrDisputeNotWritable, a dispute that takes no
// more evidence ErrDisputeClosedForEvidence. Anything else is returned as
// is (an unknown outcome the caller re-reads).
func disputeError(err error) error {
	var e *apiError
	if !errors.As(err, &e) {
		return err
	}
	if e.Status == http.StatusNotFound {
		return fmt.Errorf("%w: %s", payments.ErrDisputeNotFound, err.Error())
	}
	if e.Status == http.StatusForbidden || e.Status == http.StatusUnauthorized || e.Type == "permission_error" {
		return fmt.Errorf("%w: %s", payments.ErrDisputeNotWritable, err.Error())
	}
	if e.definitive() && e.Status == http.StatusBadRequest {
		msg := strings.ToLower(e.Message)
		if strings.Contains(msg, "already") || strings.Contains(msg, "closed") || strings.Contains(msg, "past due") || strings.Contains(msg, "no longer") {
			return fmt.Errorf("%w: %s", payments.ErrDisputeClosedForEvidence, err.Error())
		}
	}
	return err
}
