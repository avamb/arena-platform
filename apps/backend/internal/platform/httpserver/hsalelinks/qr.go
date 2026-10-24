package hsalelinks

// qr.go — task QL-5 (spec 43 section 4.2): the QR image of a sales link.
// The code encodes exactly the short address the JSON answers carry
// (salelinks.ShortURL over SHORT_LINK_BASE_URL), so a printed code and the
// link shown in the event center can never differ.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks/qr"
)

const (
	defaultQRFormat = "png"
	defaultQRSize   = 1024
	defaultQRMargin = 4
)

func badQRParams() *apiError {
	return &apiError{http.StatusBadRequest, "sales_link.invalid_qr_params",
		"format must be png or svg, size 256 to 2048 pixels, margin 2 to 8 modules"}
}

// qrParams reads ?format=&size=&margin= with their defaults.
func qrParams(r *http.Request) (format string, size, margin int, err *apiError) {
	q := r.URL.Query()
	format, size, margin = defaultQRFormat, defaultQRSize, defaultQRMargin
	if v := strings.TrimSpace(q.Get("format")); v != "" {
		format = strings.ToLower(v)
	}
	if format != "png" && format != "svg" {
		return "", 0, 0, badQRParams()
	}
	if v := strings.TrimSpace(q.Get("size")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < qr.MinSize || n > qr.MaxSize {
			return "", 0, 0, badQRParams()
		}
		size = n
	}
	if v := strings.TrimSpace(q.Get("margin")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < qr.MinMargin || n > qr.MaxMargin {
			return "", 0, 0, badQRParams()
		}
		margin = n
	}
	return format, size, margin, nil
}

// HandleQR serves GET /v1/organizations/{org_id}/sales-links/{id}/qr.
func (h *Handler) HandleQR(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.orgID(w, r)
	if !ok {
		return
	}
	id, ok := linkID(w, r)
	if !ok {
		return
	}
	format, size, margin, perr := qrParams(r)
	if perr != nil {
		perr.write(w, r)
		return
	}
	row, err := h.queries.GetSalesLinkByID(r.Context(), id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, http.StatusNotFound, "sales_link.not_found", "sales link not found")
			return
		}
		h.failed(w, r, "qr", err)
		return
	}
	content := salelinks.ShortURL(h.shortBase, row.Code)
	var (
		body []byte
		ctyp string
	)
	if format == "svg" {
		body, err = qr.SVG(content, margin)
		ctyp = "image/svg+xml; charset=utf-8"
	} else {
		body, err = qr.PNG(content, size, margin)
		ctyp = "image/png"
	}
	if err != nil {
		h.failed(w, r, "qr render", err)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	hd := w.Header()
	hd.Set("Content-Type", ctyp)
	hd.Set("Cache-Control", "private, max-age=3600")
	hd.Set("Content-Disposition", `inline; filename="`+row.Code+"."+format+`"`)
	hd.Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body) //nolint:gosec // G705 false positive: the body is an image this handler rendered itself
}

// etagMatches reports whether an If-None-Match header names etag (or is *).
func etagMatches(header, etag string) bool {
	for _, p := range strings.Split(header, ",") {
		p = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p), "W/"))
		if p == "*" || p == etag {
			return true
		}
	}
	return false
}
