package stripebilling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// Platform billing events the webhook acts on (billing task B3-3, B4).
const (
	EventCheckoutCompleted      = "checkout.session.completed"
	EventCheckoutAsyncSucceeded = "checkout.session.async_payment_succeeded"
)

// ErrInvoiceAlreadyPaid is returned by VoidInvoice when Stripe has already
// collected the invoice (a payment raced the void, 41 section 6.6).
var ErrInvoiceAlreadyPaid = errors.New("stripebilling: the Stripe invoice is already paid")

// LineItem is one priced line of a checkout or a Stripe invoice. AmountMinor
// is in Currency.
type LineItem struct {
	Name        string
	AmountMinor int64
	Currency    string
}

// CheckoutInput is a Stripe Checkout Session that pays one platform invoice.
type CheckoutInput struct {
	Currency         string
	Lines            []LineItem
	InvoiceID        string
	InvoiceNumber    string
	StripeCustomerID string
	SuccessURL       string
	CancelURL        string
	IdempotencyKey   string
}

// CreateCheckoutSession creates a hosted Stripe Checkout page (mode payment)
// for one invoice and returns its id and URL. client_reference_id and
// metadata[arena_invoice_id] name the invoice so the webhook finds it.
func (a *Adapter) CreateCheckoutSession(ctx context.Context, in CheckoutInput) (id, pageURL string, err error) {
	if len(in.Lines) == 0 || in.InvoiceID == "" || in.SuccessURL == "" || in.CancelURL == "" {
		return "", "", fmt.Errorf("stripebilling: CreateCheckoutSession: incomplete input")
	}
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("client_reference_id", in.InvoiceID)
	form.Set("success_url", in.SuccessURL)
	form.Set("cancel_url", in.CancelURL)
	form.Set("metadata[arena_invoice_id]", in.InvoiceID)
	form.Set("metadata[arena_invoice_number]", in.InvoiceNumber)
	form.Set("payment_intent_data[metadata][arena_invoice_id]", in.InvoiceID)
	if in.StripeCustomerID != "" {
		form.Set("customer", in.StripeCustomerID)
	}
	for i, l := range in.Lines {
		if l.Currency != in.Currency || l.AmountMinor <= 0 {
			return "", "", fmt.Errorf("stripebilling: CreateCheckoutSession: bad line %d", i)
		}
		p := "line_items[" + strconv.Itoa(i) + "]"
		form.Set(p+"[quantity]", "1")
		form.Set(p+"[price_data][currency]", strings.ToLower(l.Currency))
		form.Set(p+"[price_data][unit_amount]", strconv.FormatInt(l.AmountMinor, 10))
		form.Set(p+"[price_data][product_data][name]", l.Name)
	}
	raw, _, err := a.doRequest(ctx, http.MethodPost, a.cfg.BaseURL+"/checkout/sessions", form, in.IdempotencyKey)
	if err != nil {
		return "", "", fmt.Errorf("stripebilling: CreateCheckoutSession: %w", err)
	}
	var sess struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &sess); err != nil || sess.ID == "" || sess.URL == "" {
		return "", "", fmt.Errorf("stripebilling: CreateCheckoutSession: bad response")
	}
	return sess.ID, sess.URL, nil
}

// HostedInvoiceInput is a Stripe invoice that collects one platform invoice.
// The platform invoice (our PDF and number) is the legal document; the Stripe
// invoice is only the means of payment and carries our number as a
// reference (41 section 6.6).
type HostedInvoiceInput struct {
	StripeCustomerID string
	Currency         string
	Lines            []LineItem
	InvoiceID        string
	InvoiceNumber    string
	DaysUntilDue     int
	IdempotencyKey   string
}

// CreateHostedInvoice creates a Stripe invoice, attaches its lines to it
// (never to the customer's pending items), finalizes it and returns its id
// and hosted payment page.
func (a *Adapter) CreateHostedInvoice(ctx context.Context, in HostedInvoiceInput) (id, pageURL string, err error) {
	if in.StripeCustomerID == "" || len(in.Lines) == 0 || in.InvoiceID == "" {
		return "", "", fmt.Errorf("stripebilling: CreateHostedInvoice: incomplete input")
	}
	days := in.DaysUntilDue
	if days <= 0 {
		days = 14
	}
	form := url.Values{}
	form.Set("customer", in.StripeCustomerID)
	form.Set("collection_method", "send_invoice")
	form.Set("days_until_due", strconv.Itoa(days))
	form.Set("currency", strings.ToLower(in.Currency))
	form.Set("auto_advance", "false")
	form.Set("description", "Arena invoice "+in.InvoiceNumber)
	form.Set("metadata[arena_invoice_id]", in.InvoiceID)
	form.Set("metadata[arena_invoice_number]", in.InvoiceNumber)
	raw, _, err := a.doRequest(ctx, http.MethodPost, a.cfg.BaseURL+"/invoices", form, in.IdempotencyKey+"-create")
	if err != nil {
		return "", "", fmt.Errorf("stripebilling: CreateHostedInvoice create: %w", err)
	}
	var inv struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &inv); err != nil || inv.ID == "" {
		return "", "", fmt.Errorf("stripebilling: CreateHostedInvoice: bad create response")
	}
	for i, l := range in.Lines {
		item := url.Values{}
		item.Set("customer", in.StripeCustomerID)
		item.Set("invoice", inv.ID)
		item.Set("amount", strconv.FormatInt(l.AmountMinor, 10))
		item.Set("currency", strings.ToLower(l.Currency))
		item.Set("description", l.Name)
		if _, _, err := a.doRequest(ctx, http.MethodPost, a.cfg.BaseURL+"/invoiceitems", item,
			in.IdempotencyKey+"-item-"+strconv.Itoa(i)); err != nil {
			return inv.ID, "", fmt.Errorf("stripebilling: CreateHostedInvoice item %d: %w", i, err)
		}
	}
	raw, _, err = a.doRequest(ctx, http.MethodPost, a.cfg.BaseURL+"/invoices/"+inv.ID+"/finalize", url.Values{},
		in.IdempotencyKey+"-finalize")
	if err != nil {
		return inv.ID, "", fmt.Errorf("stripebilling: CreateHostedInvoice finalize: %w", err)
	}
	var fin struct {
		ID               string `json:"id"`
		HostedInvoiceURL string `json:"hosted_invoice_url"`
	}
	if err := json.Unmarshal(raw, &fin); err != nil {
		return inv.ID, "", fmt.Errorf("stripebilling: CreateHostedInvoice: bad finalize response")
	}
	return inv.ID, fin.HostedInvoiceURL, nil
}

// VoidInvoice voids a Stripe invoice. It reads the invoice first: a paid one
// answers ErrInvoiceAlreadyPaid (the caller then marks the platform invoice
// paid and corrects it by a credit note), a void one is a no-op.
func (a *Adapter) VoidInvoice(ctx context.Context, stripeInvoiceID string) error {
	raw, _, err := a.doRequest(ctx, http.MethodGet, a.cfg.BaseURL+"/invoices/"+url.PathEscape(stripeInvoiceID), nil, "")
	if err != nil {
		return fmt.Errorf("stripebilling: VoidInvoice read: %w", err)
	}
	var inv struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &inv); err != nil {
		return fmt.Errorf("stripebilling: VoidInvoice: bad read response")
	}
	switch inv.Status {
	case "paid":
		return ErrInvoiceAlreadyPaid
	case "void":
		return nil
	}
	if _, _, err := a.doRequest(ctx, http.MethodPost, a.cfg.BaseURL+"/invoices/"+url.PathEscape(stripeInvoiceID)+"/void",
		url.Values{}, "void-"+stripeInvoiceID); err != nil {
		return fmt.Errorf("stripebilling: VoidInvoice: %w", err)
	}
	return nil
}

// ExpireCheckoutSession closes an open Checkout Session so it can no longer
// be paid (the invoice was voided). An already completed or expired session
// is not an error here; the webhook decides about a completed one.
func (a *Adapter) ExpireCheckoutSession(ctx context.Context, sessionID string) error {
	_, status, err := a.doRequest(ctx, http.MethodPost,
		a.cfg.BaseURL+"/checkout/sessions/"+url.PathEscape(sessionID)+"/expire", url.Values{}, "expire-"+sessionID)
	if err != nil && status == http.StatusBadRequest {
		return nil
	}
	return err
}

// BillingEvent is a verified platform Stripe event with the fields billing
// acts on. Amounts are minor units of Currency.
type BillingEvent struct {
	ID                string
	Type              string
	ObjectID          string
	Status            string
	PaymentStatus     string
	AmountMinor       int64
	Currency          string
	ArenaInvoiceID    string
	PaymentIntentID   string
	ClientReferenceID string
}

// ParseBillingEvent verifies the Stripe-Signature against the platform
// billing webhook secret and parses the event. A checkout session reports
// amount_total, a Stripe invoice amount_paid.
func (a *Adapter) ParseBillingEvent(body []byte, sigHeader string) (*BillingEvent, error) {
	if a.cfg.WebhookSecret == "" {
		return nil, payments.ErrInvalidWebhookSignature
	}
	if err := payments.VerifyStripeSignature(sigHeader, body, a.cfg.WebhookSecret, a.cfg.WebhookTolerance); err != nil {
		return nil, err
	}
	var raw struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object struct {
				ID                string            `json:"id"`
				Status            string            `json:"status"`
				PaymentStatus     string            `json:"payment_status"`
				AmountTotal       *int64            `json:"amount_total"`
				AmountPaid        *int64            `json:"amount_paid"`
				Currency          string            `json:"currency"`
				ClientReferenceID string            `json:"client_reference_id"`
				PaymentIntent     json.RawMessage   `json:"payment_intent"`
				Metadata          map[string]string `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("stripebilling: ParseBillingEvent: %w", err)
	}
	o := raw.Data.Object
	ev := &BillingEvent{
		ID: raw.ID, Type: raw.Type, ObjectID: o.ID, Status: o.Status, PaymentStatus: o.PaymentStatus,
		Currency: strings.ToUpper(o.Currency), ArenaInvoiceID: o.Metadata["arena_invoice_id"],
		ClientReferenceID: o.ClientReferenceID,
	}
	switch {
	case o.AmountTotal != nil:
		ev.AmountMinor = *o.AmountTotal
	case o.AmountPaid != nil:
		ev.AmountMinor = *o.AmountPaid
	}
	if len(o.PaymentIntent) > 0 && o.PaymentIntent[0] == '"' {
		_ = json.Unmarshal(o.PaymentIntent, &ev.PaymentIntentID)
	}
	return ev, nil
}
