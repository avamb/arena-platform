// receipt.go — Stripe's own receipt of a paid payment (spec 51 section 2):
// the charge's receipt_url and receipt_number, plus a copy of the receipt.
//
// receipt_url expires within 30 days, while a dispute may come 120 days
// after the event, so arena downloads the document right after the payment.
// Stripe documents no download of a receipt. Its receipt page offers a PDF at
// the same path plus "/pdf", which is tried first and kept only when it really
// is a PDF; otherwise the HTML page itself is kept.
package stripe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

var _ payments.ReceiptFetcher = (*Adapter)(nil)

// receiptHost is the only host a receipt is downloaded from in production.
const receiptHost = "pay.stripe.com"

// maxReceiptBytes caps one downloaded receipt (a Stripe receipt is ~30 KB).
const maxReceiptBytes = 5 << 20

// FetchReceipt reads the latest charge of the payment and downloads its
// receipt. paymentRef is a pi_… or a hosted Checkout Session cs_…. A payment
// without a charge yet, or a charge without a receipt link, is
// payments.ErrReceiptNotReady, a payment the account does not know is
// payments.ErrReceiptNotFound. A failed download answers the link and number
// without File together with payments.ErrReceiptDownload, so the caller keeps
// the link and tries the copy again.
//
// Implements payments.ReceiptFetcher.
func (a *Adapter) FetchReceipt(ctx context.Context, paymentRef string) (payments.Receipt, error) {
	pi := strings.TrimSpace(paymentRef)
	if strings.HasPrefix(pi, "cs_") {
		resolved, err := a.ResolveChargeRef(ctx, pi)
		if err != nil {
			var declined *payments.RefundDeclinedError
			if errors.As(err, &declined) && declined.Code == "charge_ref_missing" {
				return payments.Receipt{}, payments.ErrReceiptNotReady
			}
			return payments.Receipt{}, fmt.Errorf("stripe: FetchReceipt: %w", err)
		}
		pi = resolved
	}
	if !strings.HasPrefix(pi, "pi_") {
		return payments.Receipt{}, fmt.Errorf("stripe: FetchReceipt: %q is not a payment intent", pi)
	}
	endpoint := a.cfg.BaseURL + "/payment_intents/" + url.PathEscape(pi) + "?expand[]=latest_charge"
	rawBody, _, err := a.doRequest(ctx, http.MethodGet, endpoint, nil, "")
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return payments.Receipt{}, fmt.Errorf("stripe: FetchReceipt: %w: %w", payments.ErrReceiptNotFound, err)
		}
		return payments.Receipt{}, fmt.Errorf("stripe: FetchReceipt: %w", err)
	}
	var body struct {
		LatestCharge json.RawMessage `json:"latest_charge"`
	}
	if err := json.Unmarshal(rawBody, &body); err != nil {
		return payments.Receipt{}, fmt.Errorf("stripe: FetchReceipt: unmarshal response: %w", err)
	}
	var charge struct {
		ReceiptURL    string `json:"receipt_url"`
		ReceiptNumber string `json:"receipt_number"`
	}
	if len(body.LatestCharge) == 0 || json.Unmarshal(body.LatestCharge, &charge) != nil || charge.ReceiptURL == "" {
		return payments.Receipt{}, payments.ErrReceiptNotReady
	}
	rec := payments.Receipt{URL: charge.ReceiptURL, Number: charge.ReceiptNumber}
	if !a.receiptURLAllowed(charge.ReceiptURL) {
		return rec, nil
	}
	if file, ok := a.download(ctx, receiptPDFURL(charge.ReceiptURL)); ok && bytes.HasPrefix(file.body, []byte("%PDF-")) {
		rec.File, rec.ContentType = file.body, "application/pdf"
		return rec, nil
	}
	if file, ok := a.download(ctx, charge.ReceiptURL); ok && strings.HasPrefix(file.contentType, "text/html") {
		rec.File, rec.ContentType = file.body, "text/html; charset=utf-8"
		return rec, nil
	}
	return rec, payments.ErrReceiptDownload
}

// receiptURLAllowed keeps the download on Stripe's own receipt host over
// https. A test that points the adapter at a stub API may also download from
// that stub's host.
func (a *Adapter) receiptURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return false
	}
	if u.Scheme == "https" && strings.EqualFold(u.Hostname(), receiptHost) {
		return true
	}
	return a.stubHost(u)
}

// receiptRedirectAllowed decides one redirect of a receipt download: https
// to a host of stripe.com, or the stub's host in a test.
func (a *Adapter) receiptRedirectAllowed(u *url.URL) bool {
	if u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme == "https" && (host == "stripe.com" || strings.HasSuffix(host, ".stripe.com")) {
		return true
	}
	return a.stubHost(u)
}

// stubHost reports whether u is on the stub API host a test pointed the
// adapter at. Production uses the default base URL, so it is always false.
func (a *Adapter) stubHost(u *url.URL) bool {
	if a.cfg.BaseURL == defaultBaseURL {
		return false
	}
	base, err := url.Parse(a.cfg.BaseURL)
	return err == nil && strings.EqualFold(u.Host, base.Host) && u.Scheme == base.Scheme
}

// receiptPDFURL is the PDF variant of a receipt page: "/pdf" appended to the
// path, the query kept.
func receiptPDFURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/pdf"
	return u.String()
}

type downloaded struct {
	body        []byte
	contentType string
}

// download fetches a public receipt page WITHOUT the API key (the receipt
// host needs none, and the key must never leave api.stripe.com). At most
// three redirects are followed, each only to a Stripe host over https.
func (a *Adapter) download(ctx context.Context, raw string) (downloaded, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return downloaded{}, false
	}
	client := &http.Client{
		Timeout: a.client.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || !a.receiptRedirectAllowed(req.URL) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return downloaded{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return downloaded{}, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReceiptBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxReceiptBytes {
		return downloaded{}, false
	}
	return downloaded{body: body, contentType: strings.ToLower(resp.Header.Get("Content-Type"))}, true
}
