// S3-compatible Storage implementation.
//
// This adapter targets the S3 REST API directly so the binary stays free of
// the heavyweight AWS SDK. Only the small subset of operations the media
// pipeline needs is implemented:
//
//	PUT    /<bucket>/<key>   — Put
//	GET    /<bucket>/<key>   — Get / Stat (HEAD)
//	DELETE /<bucket>/<key>   — Delete
//
// Requests are signed with AWS Signature V4. The implementation is
// compatible with AWS S3, Cloudflare R2, and MinIO out of the box; any
// service that advertises S3 v4 compatibility should also work.
//
// Path-style vs virtual-hosted-style addressing is configurable. The
// adapter defaults to path-style ("https://endpoint/bucket/key") because it
// is the lowest-common-denominator format every provider accepts; AWS S3
// itself still serves path-style requests as of 2026 for backwards
// compatibility.
//
// Concurrency: safe for use from multiple goroutines. The embedded
// http.Client is shared across requests via standard library connection
// pooling.

package storage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// S3Options carries the parameters needed to construct an S3Storage.
type S3Options struct {
	// Endpoint is the base URL (scheme + host[:port]) of the S3-compatible
	// service. Examples: "https://s3.amazonaws.com",
	// "https://<accountid>.r2.cloudflarestorage.com", "http://minio:9000".
	Endpoint string
	// Region is the AWS region string (e.g. "us-east-1"). Cloudflare R2
	// expects "auto"; MinIO accepts any non-empty value.
	Region string
	// Bucket is the bucket name. Path-style requests put it in the URL path
	// ("/<bucket>/<key>"); virtual-hosted-style places it in the host header.
	Bucket string
	// AccessKeyID and SecretAccessKey are the long-lived credentials used
	// to sign every request.
	AccessKeyID     string
	SecretAccessKey string
	// UsePathStyle forces path-style addressing. Defaults to true (see
	// package doc for rationale).
	UsePathStyle bool
	// HTTPClient is an optional override for testing. nil → http.DefaultClient.
	HTTPClient *http.Client
	// Now returns the current time and is overridable in tests. nil → time.Now.
	Now func() time.Time
}

// S3Storage implements Storage against any S3-compatible service.
type S3Storage struct {
	endpoint        *url.URL
	region          string
	bucket          string
	accessKeyID     string
	secretAccessKey string
	pathStyle       bool
	httpClient      *http.Client
	now             func() time.Time
}

// NewS3Storage validates opts and constructs a configured S3Storage.
func NewS3Storage(opts S3Options) (*S3Storage, error) {
	if opts.Endpoint == "" {
		return nil, errors.New("storage/s3: Endpoint is required")
	}
	if opts.Region == "" {
		return nil, errors.New("storage/s3: Region is required")
	}
	if opts.Bucket == "" {
		return nil, errors.New("storage/s3: Bucket is required")
	}
	if opts.AccessKeyID == "" || opts.SecretAccessKey == "" {
		return nil, errors.New("storage/s3: AccessKeyID and SecretAccessKey are required")
	}
	endpoint, err := url.Parse(opts.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("storage/s3: parse endpoint %q: %w", opts.Endpoint, err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("storage/s3: endpoint must be http(s), got %q", endpoint.Scheme)
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &S3Storage{
		endpoint:        endpoint,
		region:          opts.Region,
		bucket:          opts.Bucket,
		accessKeyID:     opts.AccessKeyID,
		secretAccessKey: opts.SecretAccessKey,
		pathStyle:       opts.UsePathStyle,
		httpClient:      hc,
		now:             now,
	}, nil
}

// Backend reports BackendS3.
func (s *S3Storage) Backend() Backend { return BackendS3 }

// urlForKey builds the absolute request URL for the given object key,
// honouring the configured path-style flag.
func (s *S3Storage) urlForKey(key string) *url.URL {
	u := *s.endpoint
	// Each path segment is escaped individually so forward slashes inside
	// the key remain literal — S3 keys use "/" as a logical separator and
	// double-escaping breaks compatibility with the AWS console.
	escapedKey := encodeS3Key(key)
	if s.pathStyle {
		u.Path = strings.TrimRight(u.Path, "/") + "/" + s.bucket + "/" + escapedKey
	} else {
		// Virtual-hosted style: prepend bucket to host.
		u.Host = s.bucket + "." + u.Host
		u.Path = strings.TrimRight(u.Path, "/") + "/" + escapedKey
	}
	return &u
}

// Put uploads in.Body to the configured bucket under in.Key.
func (s *S3Storage) Put(ctx context.Context, in PutInput) (Object, error) {
	if err := validateKey(in.Key); err != nil {
		return Object{}, err
	}
	if in.Body == nil {
		return Object{}, errors.New("storage/s3: PutInput.Body is required")
	}
	// SigV4 requires the SHA-256 of the entire payload up front, so we must
	// buffer the body. Media uploads are bounded in size (validated by the
	// HTTP layer well below 64 MiB), so this is acceptable. A future
	// streaming chunked-signing implementation can replace this if needed.
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return Object{}, fmt.Errorf("storage/s3: read upload body: %w", err)
	}
	if in.Size > 0 && int64(len(body)) != in.Size {
		return Object{}, fmt.Errorf("%w: declared=%d actual=%d", ErrSizeMismatch, in.Size, len(body))
	}

	req, err := s.newSignedRequest(ctx, http.MethodPut, in.Key, body, in.ContentType)
	if err != nil {
		return Object{}, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return Object{}, fmt.Errorf("storage/s3: PUT %s: %w", in.Key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Object{}, s.errorFromResponse("PUT", in.Key, resp)
	}
	return Object{
		Key:         in.Key,
		ContentType: in.ContentType,
		Size:        int64(len(body)),
	}, nil
}

// Get fetches an object and returns its bytes plus metadata.
func (s *S3Storage) Get(ctx context.Context, key string) (*GetResult, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	req, err := s.newSignedRequest(ctx, http.MethodGet, key, nil, "")
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("storage/s3: GET %s: %w", key, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := s.errorFromResponse("GET", key, resp)
		resp.Body.Close()
		return nil, err
	}
	size := resp.ContentLength
	if size < 0 {
		size = 0
	}
	return &GetResult{
		Object: Object{
			Key:         key,
			ContentType: resp.Header.Get("Content-Type"),
			Size:        size,
		},
		Body: resp.Body,
	}, nil
}

// Stat issues an HTTP HEAD and returns the object's metadata.
func (s *S3Storage) Stat(ctx context.Context, key string) (Object, error) {
	if err := validateKey(key); err != nil {
		return Object{}, err
	}
	req, err := s.newSignedRequest(ctx, http.MethodHead, key, nil, "")
	if err != nil {
		return Object{}, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return Object{}, fmt.Errorf("storage/s3: HEAD %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Object{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Object{}, s.errorFromResponse("HEAD", key, resp)
	}
	size := resp.ContentLength
	if size < 0 {
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			if n, perr := strconv.ParseInt(cl, 10, 64); perr == nil {
				size = n
			}
		}
	}
	return Object{
		Key:         key,
		ContentType: resp.Header.Get("Content-Type"),
		Size:        size,
	}, nil
}

// Delete removes the object. Returns ErrNotFound when the key is unknown.
func (s *S3Storage) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	req, err := s.newSignedRequest(ctx, http.MethodDelete, key, nil, "")
	if err != nil {
		return err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("storage/s3: DELETE %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	// S3 returns 204 on success, but some compatible services return 200.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return s.errorFromResponse("DELETE", key, resp)
	}
	return nil
}

// newSignedRequest constructs and signs a single S3 REST request.
//
// body may be nil for GET/HEAD/DELETE; non-nil for PUT (and any future POST).
// contentType is only meaningful for PUT.
func (s *S3Storage) newSignedRequest(
	ctx context.Context,
	method, key string,
	body []byte,
	contentType string,
) (*http.Request, error) {
	u := s.urlForKey(key)
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("storage/s3: build request: %w", err)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
	}
	if err := s.signV4(req, body); err != nil {
		return nil, err
	}
	return req, nil
}

// errorFromResponse returns a descriptive error for a non-2xx S3 response.
// The body (up to 1 KiB) is inlined so XML error codes show up in logs.
func (s *S3Storage) errorFromResponse(method, key string, resp *http.Response) error {
	const cap = 1024
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, cap))
	return fmt.Errorf(
		"storage/s3: %s %s: HTTP %d: %s",
		method, key, resp.StatusCode, strings.TrimSpace(string(snippet)),
	)
}

// ──────────────────────────────────────────────────────────────────────────────
// AWS Signature V4
// ──────────────────────────────────────────────────────────────────────────────
//
// Spec: https://docs.aws.amazon.com/general/latest/gr/sigv4_signing.html
//
// Steps performed by signV4:
//   1. Compute the SHA-256 hex digest of the payload (UNSIGNED-PAYLOAD is
//      not used — we always have the full body in memory for Put, and for
//      bodyless methods the digest of "" is well-known but recomputed for
//      clarity).
//   2. Build the canonical request (method, canonical URI, canonical query
//      string, canonical headers, signed headers, payload hash).
//   3. Build the string-to-sign (algorithm, timestamp, credential scope,
//      hash of canonical request).
//   4. Derive the signing key via the documented HMAC chain.
//   5. Compute the signature and assemble the Authorization header.

const (
	sigV4Algorithm = "AWS4-HMAC-SHA256"
	s3Service      = "s3"
)

func (s *S3Storage) signV4(req *http.Request, payload []byte) error {
	now := s.now().UTC()
	// allow:timeformat: X-Amz-Date must use the AWS SigV4 wire format.
	amzDate := now.Format("20060102T150405Z")
	// allow:timeformat: SigV4 credential scope requires the YYYYMMDD stamp.
	dateStamp := now.Format("20060102")

	payloadHash := sha256Hex(payload)

	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := canonicalQueryString(req.URL.Query())
	canonicalHeaders, signedHeaders := canonicalHeaderBlock(req.Header)

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := strings.Join([]string{dateStamp, s.region, s3Service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		sigV4Algorithm,
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(s.secretAccessKey, dateStamp, s.region, s3Service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	auth := fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		sigV4Algorithm,
		s.accessKeyID, credentialScope,
		signedHeaders,
		signature,
	)
	req.Header.Set("Authorization", auth)
	return nil
}

// canonicalQueryString builds the SigV4 canonical query string: keys sorted
// lexically, each key+value RFC 3986–escaped.
func canonicalQueryString(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		vs := values[k]
		sort.Strings(vs)
		for j, v := range vs {
			if i > 0 || j > 0 {
				b.WriteByte('&')
			}
			b.WriteString(awsURIEncode(k, true))
			b.WriteByte('=')
			b.WriteString(awsURIEncode(v, true))
		}
	}
	return b.String()
}

// canonicalHeaderBlock returns the canonical-headers and signed-headers
// segments of a SigV4 canonical request.
//
// All headers are lower-cased; values have surrounding whitespace stripped.
// The headers are emitted in lexical order with a trailing newline after
// each name:value pair (SigV4 requires an empty line at the end of the
// canonical-headers block).
func canonicalHeaderBlock(h http.Header) (string, string) {
	type kv struct{ k, v string }
	pairs := make([]kv, 0, len(h))
	for name, values := range h {
		lower := strings.ToLower(name)
		joined := strings.Join(values, ",")
		pairs = append(pairs, kv{lower, strings.TrimSpace(joined)})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })

	var canonical strings.Builder
	signed := make([]string, 0, len(pairs))
	for _, p := range pairs {
		canonical.WriteString(p.k)
		canonical.WriteByte(':')
		canonical.WriteString(p.v)
		canonical.WriteByte('\n')
		signed = append(signed, p.k)
	}
	return canonical.String(), strings.Join(signed, ";")
}

// deriveSigningKey computes the SigV4 signing key as documented in
// https://docs.aws.amazon.com/general/latest/gr/sigv4-calculate-signature.html.
func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// awsURIEncode performs the AWS-flavoured URI escaping required by SigV4.
// Differences from url.QueryEscape:
//   - space encodes as "%20" (not "+")
//   - "/" is preserved when keepSlash is true (used for object keys)
//   - the unreserved set is exactly [A-Za-z0-9-_.~]
func awsURIEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// encodeS3Key escapes an object key for use in the request URL path. Forward
// slashes in the key represent logical S3 folders and are preserved.
func encodeS3Key(key string) string {
	return awsURIEncode(key, false)
}

// compile-time assertion
var _ Storage = (*S3Storage)(nil)
