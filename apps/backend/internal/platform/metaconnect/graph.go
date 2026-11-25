package metaconnect

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultGraphBaseURL is the Graph API host used when META_GRAPH_BASE_URL is
// unset (the only one production accepts).
const DefaultGraphBaseURL = "https://graph.facebook.com"

// DefaultAssetEdges is the list of Graph edges read after a connection
// (spec 66 section 4.1a). {business} is the client business id from GET /me;
// an edge that needs it is skipped when /me names no business.
const DefaultAssetEdges = "dataset:{business}/owned_pixels,dataset:{business}/client_pixels," +
	"ad_account:{business}/owned_ad_accounts,ad_account:{business}/client_ad_accounts,ad_account:me/adaccounts," +
	"page:{business}/owned_pages,page:{business}/client_pages,page:me/accounts"

// Asset kinds, as stored in org_meta_assets.kind.
const (
	KindDataset   = "dataset"
	KindAdAccount = "ad_account"
	KindPage      = "page"
)

const (
	maxGraphBody  = 1 << 20
	maxEdgePages  = 10
	edgePageLimit = "100"
)

// GraphError is an error answer of the Graph API. It never carries the
// message text Meta sent, only the codes.
type GraphError struct {
	HTTPStatus int
	Code       int
	Subcode    int
	Type       string
}

func (e *GraphError) Error() string {
	return fmt.Sprintf("metaconnect: graph error code %d subcode %d type %q (http %d)", e.Code, e.Subcode, e.Type, e.HTTPStatus)
}

// IsTokenInvalid reports whether err is a Graph error 190 (the access token
// is invalid, expired or revoked): only this one puts a connection in error.
func IsTokenInvalid(err error) bool {
	var ge *GraphError
	return errors.As(err, &ge) && ge.Code == 190
}

// IsPermissionError reports a Graph error 10, 100 or 200-299: no permission
// or no access to that object. It never breaks a connection.
func IsPermissionError(err error) bool {
	var ge *GraphError
	if !errors.As(err, &ge) {
		return false
	}
	return ge.Code == 10 || ge.Code == 100 || (ge.Code >= 200 && ge.Code <= 299)
}

// ErrorCode is a short code of err for a stored last_error or a redirect
// reason: "graph_<code>" for a Graph error, "graph_unavailable" otherwise.
func ErrorCode(err error) string {
	var ge *GraphError
	if errors.As(err, &ge) {
		return "graph_" + strconv.Itoa(ge.Code)
	}
	return "graph_unavailable"
}

// Client calls the Graph API. Access tokens travel in the Authorization
// header only. The code exchange is the one call whose URL carries secrets
// (the app secret and the code, as Meta documents it), so no error of do
// ever repeats a URL.
type Client struct {
	baseURL   string
	version   string
	appID     string
	appSecret string
	http      *http.Client
}

// NewClient builds a Client. An empty baseURL means DefaultGraphBaseURL; a
// nil hc gets a 15-second timeout.
func NewClient(baseURL, version, appID, appSecret string, hc *http.Client) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultGraphBaseURL
	}
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{baseURL: baseURL, version: strings.TrimSpace(version), appID: appID, appSecret: appSecret, http: hc}
}

// Token is an access token from the code exchange. Its String and JSON forms
// never show the value.
type Token struct {
	AccessToken string
	// ExpiresAt is nil when Meta sends no expires_in (a BISU token does not
	// expire by default).
	ExpiresAt *time.Time
}

func (Token) String() string { return "metaconnect.Token{redacted}" }

// MarshalJSON hides the value.
func (Token) MarshalJSON() ([]byte, error) { return []byte(`"redacted"`), nil }

// Me is GET /me?fields=id,client_business_id.
type Me struct {
	ID               string
	ClientBusinessID string
}

// Asset is one dataset, ad account or page.
type Asset struct {
	Kind     string
	MetaID   string
	Name     string
	Currency string
	Timezone string
}

// EdgeSkip is an edge that was not read, with the reason ("no_business" or
// ErrorCode of the Graph answer).
type EdgeSkip struct {
	Edge   string
	Reason string
}

// Edge is one entry of the asset edge list.
type Edge struct {
	Kind string
	Path string
}

func (e Edge) String() string { return e.Kind + ":" + e.Path }

var edgePathRe = regexp.MustCompile(`^(\{business\}|me|[0-9]+)(/[a-z_]+)+$`)

// ParseEdges reads a list "kind:path,kind:path". An empty spec means
// DefaultAssetEdges.
func ParseEdges(spec string) ([]Edge, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		spec = DefaultAssetEdges
	}
	var out []Edge
	seen := map[string]bool{}
	for i, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kind, path, ok := strings.Cut(part, ":")
		kind, path = strings.TrimSpace(kind), strings.Trim(strings.TrimSpace(path), "/")
		if !ok || (kind != KindDataset && kind != KindAdAccount && kind != KindPage) {
			return nil, fmt.Errorf("metaconnect: edge %d needs a kind dataset, ad_account or page", i+1)
		}
		if !edgePathRe.MatchString(path) {
			return nil, fmt.Errorf("metaconnect: edge %d has an invalid path", i+1)
		}
		e := Edge{Kind: kind, Path: path}
		if !seen[e.String()] {
			seen[e.String()] = true
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("metaconnect: the edge list is empty")
	}
	return out, nil
}

func (c *Client) endpoint(path string) string {
	return c.baseURL + "/" + c.version + "/" + strings.TrimPrefix(path, "/")
}

// do sends one request. token goes to the Authorization header; form, when
// not nil, is the POST body. out receives the JSON answer.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, form url.Values, token string, out any) error {
	u := c.endpoint(path)
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return errors.New("metaconnect: cannot build the graph request")
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// Never wrap err: a *url.Error repeats the address.
		if ctx.Err() != nil {
			return errors.New("metaconnect: graph request cancelled or timed out")
		}
		return errors.New("metaconnect: graph request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxGraphBody))
	if err != nil {
		return errors.New("metaconnect: cannot read the graph answer")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return parseGraphError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("metaconnect: graph answer is not valid JSON")
	}
	return nil
}

func parseGraphError(status int, raw []byte) error { return ParseGraphError(status, raw) }

// ParseGraphError reads the codes of a Graph error answer (status is the HTTP
// status, raw the body). The message text is dropped: only the code, the
// subcode and a short type are kept, so the result is safe to log or store.
func ParseGraphError(status int, raw []byte) *GraphError {
	var env struct {
		Error struct {
			Code    int    `json:"code"`
			Subcode int    `json:"error_subcode"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	ge := &GraphError{HTTPStatus: status}
	if json.Unmarshal(raw, &env) == nil {
		ge.Code, ge.Subcode = env.Error.Code, env.Error.Subcode
		if len(env.Error.Type) <= 64 {
			ge.Type = env.Error.Type
		}
	}
	return ge
}

// ExchangeCode trades a login code for an access token. redirectURI is
// META_OAUTH_REDIRECT_URL, the address the dialog/oauth login used.
// Meta documents this call as GET /oauth/access_token with query parameters,
// so the app secret and the code travel in the URL here: do never puts the
// URL (a *url.Error) or the answer body into an error, and nothing logs it.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (Token, error) {
	query := url.Values{
		"client_id":     {c.appID},
		"client_secret": {c.appSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	var out struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := c.do(ctx, http.MethodGet, "oauth/access_token", query, nil, "", &out); err != nil {
		return Token{}, err
	}
	if out.AccessToken == "" {
		return Token{}, errors.New("metaconnect: the code exchange returned no token")
	}
	t := Token{AccessToken: out.AccessToken}
	if n, err := strconv.ParseInt(jsonID(out.ExpiresIn), 10, 64); err == nil && n > 0 {
		exp := time.Now().UTC().Add(time.Duration(n) * time.Second)
		t.ExpiresAt = &exp
	}
	return t, nil
}

// Me reads GET /me?fields=id,client_business_id.
func (c *Client) Me(ctx context.Context, token string) (Me, error) {
	var out struct {
		ID               json.RawMessage `json:"id"`
		ClientBusinessID json.RawMessage `json:"client_business_id"`
	}
	if err := c.do(ctx, http.MethodGet, "me", url.Values{"fields": {"id,client_business_id"}}, nil, token, &out); err != nil {
		return Me{}, err
	}
	me := Me{ID: jsonID(out.ID), ClientBusinessID: jsonID(out.ClientBusinessID)}
	if me.ID == "" {
		return Me{}, errors.New("metaconnect: GET /me returned no id")
	}
	return me, nil
}

// Permissions lists the granted permissions of GET /me/permissions.
func (c *Client) Permissions(ctx context.Context, token string) ([]string, error) {
	var out struct {
		Data []struct {
			Permission string `json:"permission"`
			Status     string `json:"status"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "me/permissions", nil, nil, token, &out); err != nil {
		return nil, err
	}
	perms := []string{}
	for _, p := range out.Data {
		if p.Status == "granted" && p.Permission != "" && len(p.Permission) <= 128 {
			perms = append(perms, p.Permission)
		}
	}
	return perms, nil
}

// Assets reads every edge and returns the assets, de-duplicated by kind and
// id in edge order. An edge that needs {business} while businessID is empty,
// or that answers a permission error (10, 100, 200-299), is skipped and
// reported; any other failure ends the call.
func (c *Client) Assets(ctx context.Context, token, businessID string, edges []Edge) ([]Asset, []EdgeSkip, error) {
	var assets []Asset
	var skips []EdgeSkip
	seen := map[string]bool{}
	for _, e := range edges {
		path := e.Path
		if strings.Contains(path, "{business}") {
			if businessID == "" {
				skips = append(skips, EdgeSkip{Edge: e.String(), Reason: "no_business"})
				continue
			}
			path = strings.ReplaceAll(path, "{business}", url.PathEscape(businessID))
		}
		got, err := c.edge(ctx, token, e.Kind, path)
		if err != nil {
			if IsPermissionError(err) {
				skips = append(skips, EdgeSkip{Edge: e.String(), Reason: ErrorCode(err)})
				continue
			}
			return nil, nil, err
		}
		for _, a := range got {
			key := a.Kind + "\x00" + a.MetaID
			if !seen[key] {
				seen[key] = true
				assets = append(assets, a)
			}
		}
	}
	return assets, skips, nil
}

func (c *Client) edge(ctx context.Context, token, kind, path string) ([]Asset, error) {
	fields := "id,name"
	if kind == KindAdAccount {
		fields = "id,name,currency,timezone_name"
	}
	var out []Asset
	after := ""
	for page := 0; page < maxEdgePages; page++ {
		q := url.Values{"fields": {fields}, "limit": {edgePageLimit}}
		if after != "" {
			q.Set("after", after)
		}
		var resp struct {
			Data []struct {
				ID           json.RawMessage `json:"id"`
				Name         string          `json:"name"`
				Currency     string          `json:"currency"`
				TimezoneName string          `json:"timezone_name"`
			} `json:"data"`
			Paging struct {
				Cursors struct {
					After string `json:"after"`
				} `json:"cursors"`
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := c.do(ctx, http.MethodGet, path, q, nil, token, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			id := jsonID(d.ID)
			if id == "" || len(id) > 64 {
				continue
			}
			out = append(out, Asset{Kind: kind, MetaID: id, Name: clip(d.Name, 256), Currency: clip(d.Currency, 8), Timezone: clip(d.TimezoneName, 64)})
		}
		if resp.Paging.Next == "" || resp.Paging.Cursors.After == "" || resp.Paging.Cursors.After == after {
			break
		}
		after = resp.Paging.Cursors.After
	}
	return out, nil
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "")
	}
	return s
}

// PagePreview is the live preview of a Page (GET /{page_id}). It is read on
// request and never stored or logged.
type PagePreview struct {
	ID         string
	Name       string
	Category   string
	Link       string
	FanCount   *int64
	PictureURL string
}

var pageIDRe = regexp.MustCompile(`^[0-9]{1,32}$`)

// ErrBadPageID means the page id is not a Meta numeric id; nothing was sent.
var ErrBadPageID = errors.New("metaconnect: the page id is not a Meta id")

// Page reads GET /{page_id}?fields=name,category,link,fan_count,picture{url}
// with the connection token (pages_read_engagement).
func (c *Client) Page(ctx context.Context, token, pageID string) (PagePreview, error) {
	if !pageIDRe.MatchString(pageID) {
		return PagePreview{}, ErrBadPageID
	}
	var out struct {
		ID       json.RawMessage `json:"id"`
		Name     string          `json:"name"`
		Category string          `json:"category"`
		Link     string          `json:"link"`
		FanCount *int64          `json:"fan_count"`
		Picture  struct {
			Data struct {
				URL string `json:"url"`
			} `json:"data"`
		} `json:"picture"`
	}
	q := url.Values{"fields": {"name,category,link,fan_count,picture{url}"}}
	if err := c.do(ctx, http.MethodGet, pageID, q, nil, token, &out); err != nil {
		return PagePreview{}, err
	}
	p := PagePreview{ID: jsonID(out.ID), Name: clip(out.Name, 256), Category: clip(out.Category, 128), FanCount: out.FanCount}
	if p.ID == "" {
		p.ID = pageID
	}
	if u, err := url.Parse(strings.TrimSpace(out.Link)); err == nil && u.Scheme == "https" && u.Host != "" && len(out.Link) <= 512 {
		p.Link = u.String()
	}
	if raw := strings.TrimSpace(out.Picture.Data.URL); len(raw) <= 2048 {
		if u, err := url.Parse(raw); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			p.PictureURL = u.String()
		}
	}
	return p, nil
}

// maxPictureBytes is the largest Page picture PictureDataURL reads.
const maxPictureBytes = 256 << 10

// pictureHostAllowed reports whether a picture URL points at Meta's CDN
// (https, *.fbcdn.net or *.facebook.com) or, with the test seam, at the
// Graph host itself.
func (c *Client) pictureHostAllowed(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	if u.Scheme == "https" && (strings.HasSuffix(host, ".fbcdn.net") || strings.HasSuffix(host, ".facebook.com")) {
		return true
	}
	if c.baseURL == DefaultGraphBaseURL {
		return false
	}
	b, err := url.Parse(c.baseURL)
	return err == nil && b.Scheme == u.Scheme && strings.EqualFold(b.Host, u.Host)
}

// PictureDataURL downloads a Page picture and answers it as a data: URL, so
// the event center shows it without loading a third-party host (its CSP
// allows images from itself and data: only). Only Meta's CDN is fetched,
// redirects stay on it, at most 256 KB of image/jpeg, png, gif or webp.
// Any failure answers "" and an error without the URL.
func (c *Client) PictureDataURL(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || !c.pictureHostAllowed(u) {
		return "", errors.New("metaconnect: the picture is not on Meta's CDN")
	}
	hc := *c.http
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !c.pictureHostAllowed(req.URL) {
			return errors.New("metaconnect: picture redirect refused")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", errors.New("metaconnect: cannot build the picture request")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", errors.New("metaconnect: picture request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("metaconnect: picture not available")
	}
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]))
	switch ct {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return "", errors.New("metaconnect: the picture is not an image")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPictureBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxPictureBytes {
		return "", errors.New("metaconnect: the picture is empty or too large")
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(body), nil
}

// RevokePermissions asks Meta to drop the app's access (DELETE
// /me/permissions). Best effort: the caller wipes its token either way.
func (c *Client) RevokePermissions(ctx context.Context, token string) error {
	return c.do(ctx, http.MethodDelete, "me/permissions", nil, nil, token, nil)
}
