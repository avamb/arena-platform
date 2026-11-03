package eventpage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // DecodeConfig of a JPEG picture
	_ "image/png"  // DecodeConfig of a PNG picture
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html/charset"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/egress"
)

// Fetch limits (spec 59 §8).
const (
	// PageTimeout bounds one page request, redirects included.
	PageTimeout = 10 * time.Second
	// MaxPageBytes caps the HTML read from a page.
	MaxPageBytes = 2 << 20
	// ImageTimeout bounds one picture download.
	ImageTimeout = 15 * time.Second
	// MaxImageBytes caps a picture, the same 5 MiB a poster upload may have.
	MaxImageBytes = 5 << 20
	// MaxImagePixels refuses a picture too large to decode safely (the web
	// poster check refuses the same).
	MaxImagePixels = 25_000_000
	// UserAgent names the platform to the organizer's server.
	UserAgent = "ArenaSoldOutBot/1.0 (+https://arenasoldout.com/bot)"
)

// Errors of a fetch. Their text never names the address.
var (
	ErrInvalidURL        = errors.New("eventpage: the address is not a valid http(s) address")
	ErrNotAllowed        = errors.New("eventpage: the address is not on the public internet")
	ErrTicketingPlatform = errors.New("eventpage: the address is a page of a ticketing platform")
	ErrNotHTML           = errors.New("eventpage: the address is not a web page")
	ErrTooLarge          = errors.New("eventpage: the page or picture is too large")
	ErrUnreachable       = errors.New("eventpage: the site did not answer")
	ErrNotImage          = errors.New("eventpage: the picture is not a JPEG or PNG image")
	errTooManyRedirects  = errors.New("eventpage: too many redirects")
)

// UpstreamError is a page that answered with an HTTP error status.
type UpstreamError struct{ Status int }

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("eventpage: the site answered HTTP %d", e.Status)
}

// Page is a fetched web page, its body already UTF-8.
type Page struct {
	// FinalURL is the address after redirects.
	FinalURL *url.URL
	HTML     []byte
}

// Fetcher downloads pages and pictures through one egress policy.
type Fetcher struct {
	policy egress.Policy
	page   *http.Client
	img    *http.Client
}

// NewFetcher builds a fetcher on policy (egress.Fetch in production; a test
// pins egress.PrivateDeny to prove a refusal).
func NewFetcher(policy egress.Policy) *Fetcher {
	page := policy.Client(PageTimeout)
	// Every redirect is checked against the ticketing list as well: a short
	// link on the organizer's domain must not lead to a ticketing page.
	guard := page.CheckRedirect
	page.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if guard != nil {
			if err := guard(req, via); err != nil {
				if errors.Is(err, http.ErrUseLastResponse) {
					return errTooManyRedirects
				}
				return err
			}
		}
		if IsTicketingHost(req.URL.Hostname()) {
			return ErrTicketingPlatform
		}
		return nil
	}
	return &Fetcher{policy: policy, page: page, img: policy.Client(ImageTimeout)}
}

// urlShape is the only address shape a request is ever built from: http or
// https, a host with no user info, spaces or backslashes, then an optional
// path and query. The egress policy still decides where it may go.
var urlShape = regexp.MustCompile(`(?i)^https?://[^\s/?#@\\]+(?:[/?][^\s#]*)?$`)

// shapedURL parses raw after the shape check above.
func shapedURL(raw string) (*url.URL, error) {
	if !urlShape.MatchString(raw) {
		return nil, ErrInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrInvalidURL
	}
	return u, nil
}

// CheckURL validates a user's page address without fetching it: an
// absolute http(s) address on a public host, not a ticketing platform. The
// returned URL is the parsed address.
func (f *Fetcher) CheckURL(ctx context.Context, raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return nil, ErrInvalidURL
	}
	// A page address pasted with its fragment (#tickets) is common; the
	// fragment never reaches the server anyway.
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = raw[:i]
	}
	u, err := shapedURL(raw)
	if err != nil {
		return nil, err
	}
	if IsTicketingHost(u.Hostname()) {
		return nil, ErrTicketingPlatform
	}
	switch err := f.policy.CheckURL(ctx, u.String()); {
	case err == nil:
		return u, nil
	case errors.Is(err, egress.ErrDestinationNotAllowed):
		return nil, ErrNotAllowed
	default:
		return nil, ErrInvalidURL
	}
}

// FetchPage downloads the page at raw. It returns one of the errors above,
// an *UpstreamError, or the page.
func (f *Fetcher) FetchPage(ctx context.Context, raw string) (Page, error) {
	u, err := f.CheckURL(ctx, raw)
	if err != nil {
		return Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, PageTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, ErrInvalidURL
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1")
	res, err := f.page.Do(req)
	if err != nil {
		return Page{}, classifyTransport(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		// A redirect the client did not follow (no Location, or past the
		// policy's limit): there is no page to read.
		return Page{}, ErrUnreachable
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return Page{}, &UpstreamError{Status: res.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxPageBytes+1))
	if err != nil {
		return Page{}, ErrUnreachable
	}
	if len(body) > MaxPageBytes {
		return Page{}, ErrTooLarge
	}
	contentType := res.Header.Get("Content-Type")
	if !isHTMLType(contentType, body) {
		return Page{}, ErrNotHTML
	}
	utf8Body, err := toUTF8(body, contentType)
	if err != nil {
		return Page{}, ErrNotHTML
	}
	return Page{FinalURL: res.Request.URL, HTML: utf8Body}, nil
}

// Picture is a downloaded JPEG or PNG.
type Picture struct {
	Data        []byte
	ContentType string // image/jpeg or image/png
	Width       int
	Height      int
}

// FetchImage downloads a picture. Only JPEG and PNG are accepted, decided by
// the bytes, never by the server's header.
func (f *Fetcher) FetchImage(ctx context.Context, raw string) (Picture, error) {
	u, err := shapedURL(strings.TrimSpace(raw))
	if err != nil {
		return Picture{}, err
	}
	if err := f.policy.CheckURL(ctx, u.String()); err != nil {
		if errors.Is(err, egress.ErrDestinationNotAllowed) {
			return Picture{}, ErrNotAllowed
		}
		return Picture{}, ErrInvalidURL
	}
	ctx, cancel := context.WithTimeout(ctx, ImageTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Picture{}, ErrInvalidURL
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "image/jpeg,image/png;q=0.9,*/*;q=0.1")
	res, err := f.img.Do(req)
	if err != nil {
		return Picture{}, classifyTransport(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return Picture{}, &UpstreamError{Status: res.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxImageBytes+1))
	if err != nil {
		return Picture{}, ErrUnreachable
	}
	if len(data) > MaxImageBytes {
		return Picture{}, ErrTooLarge
	}
	return DecodePicture(data)
}

// DecodePicture checks bytes are a JPEG or PNG of a sane size and reads its
// dimensions.
func DecodePicture(data []byte) (Picture, error) {
	if len(data) == 0 {
		return Picture{}, ErrNotImage
	}
	ct := http.DetectContentType(data)
	if ct != "image/jpeg" && ct != "image/png" {
		return Picture{}, ErrNotImage
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || cfg.Width <= 0 || cfg.Height <= 0 {
		return Picture{}, ErrNotImage
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxImagePixels {
		return Picture{}, ErrTooLarge
	}
	return Picture{Data: data, ContentType: ct, Width: cfg.Width, Height: cfg.Height}, nil
}

// FitsPoster applies the Telegram bot's poster rules: 4:5 or square within
// 2 %, at least 1000 px wide.
func FitsPoster(w, h int) bool {
	if w < 1000 || h <= 0 {
		return false
	}
	ratio := float64(w) / float64(h)
	near := func(want float64) bool { return ratio > want*0.98 && ratio < want*1.02 }
	return near(0.8) || near(1.0)
}

// classifyTransport maps a client error to a sentinel. A refused
// destination is told apart; every other failure (timeout, DNS, TLS, reset)
// is "the site did not answer", never the transport's own text.
func classifyTransport(err error) error {
	switch {
	case errors.Is(err, ErrTicketingPlatform):
		return ErrTicketingPlatform
	case errors.Is(err, egress.ErrDestinationNotAllowed):
		return ErrNotAllowed
	case errors.Is(err, egress.ErrSchemeNotAllowed):
		return ErrInvalidURL
	case errors.Is(err, errTooManyRedirects):
		return ErrUnreachable
	default:
		return ErrUnreachable
	}
}

// isHTMLType accepts text/html and application/xhtml+xml; a response with
// no type is sniffed.
func isHTMLType(header string, body []byte) bool {
	mt, _, err := mime.ParseMediaType(header)
	if err != nil || mt == "" {
		mt, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	return mt == "text/html" || mt == "application/xhtml+xml"
}

// toUTF8 decodes the body with the charset of the header, a <meta charset>
// or a byte-order mark; an unknown charset reads as UTF-8.
func toUTF8(body []byte, contentType string) ([]byte, error) {
	r, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return body, nil
	}
	out, err := io.ReadAll(io.LimitReader(r, 4*MaxPageBytes))
	if err != nil {
		return nil, err
	}
	return out, nil
}
