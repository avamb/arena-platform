// Package generator is the HTTP client of the venue SVG generator
// (apps/venue-svg-generator, contract HALL_MODEL.md section 7). Only
// arena-worker uses it (spec 64 wave 2): the job venue_plan.generate sends the
// files of an intake with Generate, and Render is the venueplan.Renderer seam
// for the answers loop of a later wave.
//
// The worker job uses two short calls: Submit sends the files (POST /generate,
// multipart, timeout CallTimeout) and returns the generator's job id; Poll asks
// once about the job (GET /jobs/{id}, timeout PollTimeout). The job keeps the
// waiting between the calls in worker_jobs rows, so no call holds the worker
// loop for long. Generate is Submit plus a polling loop in one call (tests and
// tools): one Client lets one Generate run at a time, under Deadline.
//
// Errors are *Error (Msg may carry the generator's own text: log Code only):
// Retry tells the caller whether a later attempt may
// succeed (network, 5xx, 503 queue.full or busy, a job failed by timeout or a
// crash, the poll deadline) or not (401, 400, 413, a job failed by
// memory_limit).
package generator

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/intake"
)

// Kind is the declared kind of one file sent to POST /generate.
type Kind string

// The kinds of HALL_MODEL.md section 7.
const (
	KindXLSX  Kind = "xlsx"
	KindPDF   Kind = "pdf"
	KindImage Kind = "image"
	KindOther Kind = "other"
)

// Limits of the generator's POST /generate (VENUE_GEN_MAX_UPLOAD_MB = 10, the
// body at most three times that, 1..10 files).
const (
	MaxFiles     = 10
	MaxFileBytes = 10 << 20
	MaxBodyBytes = 30 << 20
)

// Defaults of a Client.
const (
	DefaultCallTimeout  = 120 * time.Second
	DefaultPollTimeout  = 30 * time.Second
	DefaultDeadline     = 4 * time.Minute
	DefaultPollInterval = 2 * time.Second
)

// Limits of what the generator may answer with, checked before anything is
// parsed or stored.
const (
	// MaxSVGBytes is the biggest authoring SVG taken: the limit the API
	// applies to an uploaded draft SVG (venueplan.MaxDraftSVGBytes).
	MaxSVGBytes = venueplan.MaxDraftSVGBytes
	// MaxPreviewBytes is the biggest preview PNG taken. The intake has no
	// limit of its own for a preview, so this is a stated one: a preview is a
	// thumbnail of the plan and 10 MB is the generator's own upload limit.
	MaxPreviewBytes = 10 << 20
	// maxJobIDLen is the longest generator job id accepted.
	maxJobIDLen = 200
	// maxAnswerBytes caps the raw HTTP answer: the SVG (JSON escaping at most
	// doubles it), the preview in base64, and 4 MB for the model, the file
	// reports and the JSON around them (about 23 MB in all).
	maxAnswerBytes = 2*MaxSVGBytes + (MaxPreviewBytes+2)/3*4 + 4<<20
)

// File is one file to send.
type File struct {
	Name        string
	Kind        Kind
	ContentType string
	Data        []byte
}

// FileReport is the generator's report of one sent file.
type FileReport struct {
	Index            int     `json:"index"`
	DeclaredKind     string  `json:"declared_kind"`
	DetectedKind     string  `json:"detected_kind"`
	Adapter          *string `json:"adapter"`
	Outcome          string  `json:"outcome"`
	Reason           *string `json:"reason"`
	SeatCount        *int    `json:"seat_count"`
	ValidationStatus *string `json:"validation_status"`
}

// Result is a job that ended with status done.
type Result struct {
	JobID string
	// Result is "ok" or "no_adapter".
	Result string
	// ValidationStatus is PASS or QUARANTINE; empty with no_adapter.
	ValidationStatus string
	// GeneratorVersion is the generator_version object, compact JSON.
	GeneratorVersion string
	Files            []FileReport
	// Model is the seating-json-v2 model; nil with no_adapter.
	Model json.RawMessage
	// SVG is the authoring SVG; nil when no seat has an identity.
	SVG []byte
	// PreviewPNG may be nil.
	PreviewPNG []byte
	// SVGTooLarge is the size in bytes of an authoring SVG over MaxSVGBytes
	// (SVG is then nil); 0 when the SVG was within the limit.
	SVGTooLarge int
	// PreviewTooLarge is the size of a preview over MaxPreviewBytes, decoded
	// (PreviewPNG is then nil); 0 when within the limit.
	PreviewTooLarge int
}

// Result values.
const (
	ResultOK        = "ok"
	ResultNoAdapter = "no_adapter"
)

// Validation statuses.
const (
	StatusPass       = "PASS"
	StatusQuarantine = "QUARANTINE"
)

// Error is a failed call. Code is the generator's error code (or a client
// code: network, timeout, bad_answer, job_lost); Status the HTTP status, 0
// when there was no answer.
type Error struct {
	Status int
	Code   string
	Msg    string
	Retry  bool
}

func (e *Error) Error() string {
	kind := "final"
	if e.Retry {
		kind = "retry"
	}
	if e.Status != 0 {
		return fmt.Sprintf("venue generator (%s): HTTP %d %s: %s", kind, e.Status, e.Code, e.Msg)
	}
	return fmt.Sprintf("venue generator (%s): %s: %s", kind, e.Code, e.Msg)
}

// Retryable reports whether err is a generator error a later attempt may
// overcome. A non-generator error (a context of the caller) counts as
// retryable.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	var ge *Error
	if errors.As(err, &ge) {
		return ge.Retry
	}
	return true
}

// Client talks to one generator.
type Client struct {
	base         string
	token        string
	http         *http.Client
	callTimeout  time.Duration
	pollTimeout  time.Duration
	deadline     time.Duration
	pollInterval time.Duration
	sem          chan struct{}
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client (tests).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithCallTimeout sets the timeout of one HTTP call (POST /generate, POST
// /render).
func WithCallTimeout(d time.Duration) Option { return func(c *Client) { c.callTimeout = d } }

// WithPollTimeout sets the timeout of one GET /jobs/{id}.
func WithPollTimeout(d time.Duration) Option { return func(c *Client) { c.pollTimeout = d } }

// WithDeadline sets the deadline of one Generate (waiting for the turn,
// submit and poll).
func WithDeadline(d time.Duration) Option { return func(c *Client) { c.deadline = d } }

// WithPollInterval sets the wait between two GET /jobs/{id}.
func WithPollInterval(d time.Duration) Option { return func(c *Client) { c.pollInterval = d } }

// New builds a Client for the generator at baseURL (an origin such as
// http://venue-gen:8090) with its bearer token.
func New(baseURL, token string, opts ...Option) *Client {
	c := &Client{
		base:         strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:        strings.TrimSpace(token),
		callTimeout:  DefaultCallTimeout,
		pollTimeout:  DefaultPollTimeout,
		deadline:     DefaultDeadline,
		pollInterval: DefaultPollInterval,
		sem:          make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(c)
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	return c
}

var _ venueplan.Renderer = (*Client)(nil)

// Generate sends the files and waits for the job. A 404 of the job (the
// generator restarted and lost it) sends the files again, once.
func (c *Client) Generate(ctx context.Context, files []File, opts ...SubmitOption) (Result, error) {
	if len(files) == 0 || len(files) > MaxFiles {
		return Result{}, &Error{Code: "request.invalid", Msg: fmt.Sprintf("send 1..%d files, got %d", MaxFiles, len(files))}
	}
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return Result{}, &Error{Code: "timeout", Msg: "waited too long for the generator's turn", Retry: true}
	}
	defer func() { <-c.sem }()

	resent := false
	for {
		jobID, err := c.Submit(ctx, files, opts...)
		if err != nil {
			return Result{}, err
		}
		res, err := c.wait(ctx, jobID)
		if IsJobLost(err) && !resent {
			resent = true
			continue
		}
		return res, err
	}
}

// IsJobLost reports whether err is the generator's 404 of a job (it
// restarted and lost the job).
func IsJobLost(err error) bool {
	var ge *Error
	return errors.As(err, &ge) && ge.Code == CodeJobNotFound
}

// CodeJobNotFound is the code of Poll's error for a job the generator does
// not know.
const CodeJobNotFound = "job.not_found"

// SubmitOption changes one POST /generate.
type SubmitOption func(*submitOpts)

type submitOpts struct{ categoryName string }

// WithCategoryName sends the multipart field target_profile: the generator's
// default authoring profile (arena-authoring-v1) with only category.name
// replaced, so the draft carries a human category name instead of the
// profile's PLACEHOLDER. An empty name sends no profile.
func WithCategoryName(name string) SubmitOption {
	return func(o *submitOpts) { o.categoryName = strings.TrimSpace(name) }
}

// TargetProfile is the JSON of the target_profile field for a category name:
// the default profile of apps/venue-svg-generator/arena_export/profiles/
// arena-authoring-v1.json with category.name replaced.
func TargetProfile(categoryName string) []byte {
	p := map[string]any{
		"profile_id":      "arena-authoring-v1",
		"target":          "arena-go-importsvg",
		"commercial_data": false,
		"category":        map[string]any{"name": categoryName, "color": "#00aaff", "index": 1},
		"stroke_width_px": 3,
		"legend":          []string{"Sold", "None", "Reserved", "MyTickets"},
	}
	raw, _ := json.Marshal(p)
	return raw
}

// Submit sends the files (POST /generate) and returns the generator's job id.
func (c *Client) Submit(ctx context.Context, files []File, opts ...SubmitOption) (string, error) {
	if len(files) == 0 || len(files) > MaxFiles {
		return "", &Error{Code: "request.invalid", Msg: fmt.Sprintf("send 1..%d files, got %d", MaxFiles, len(files))}
	}
	return c.submit(ctx, files, opts)
}

func (c *Client) submit(ctx context.Context, files []File, opts []SubmitOption) (string, error) {
	var so submitOpts
	for _, o := range opts {
		o(&so)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	kinds := make([]Kind, 0, len(files))
	for _, f := range files {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="%s"`, quoteName(f.Name)))
		ct := f.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		h.Set("Content-Type", ct)
		w, err := mw.CreatePart(h)
		if err != nil {
			return "", &Error{Code: "request.invalid", Msg: err.Error()}
		}
		if _, err := w.Write(f.Data); err != nil {
			return "", &Error{Code: "request.invalid", Msg: err.Error()}
		}
		kinds = append(kinds, f.Kind)
	}
	rawKinds, _ := json.Marshal(kinds)
	if err := mw.WriteField("kinds", string(rawKinds)); err != nil {
		return "", &Error{Code: "request.invalid", Msg: err.Error()}
	}
	if so.categoryName != "" {
		if err := mw.WriteField("target_profile", string(TargetProfile(so.categoryName))); err != nil {
			return "", &Error{Code: "request.invalid", Msg: err.Error()}
		}
	}
	if err := mw.Close(); err != nil {
		return "", &Error{Code: "request.invalid", Msg: err.Error()}
	}
	status, raw, err := c.call(ctx, c.callTimeout, http.MethodPost, "/generate", mw.FormDataContentType(), body.Bytes())
	if err != nil {
		return "", err
	}
	if status != http.StatusAccepted && status != http.StatusOK {
		return "", answerError(status, raw)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || !validJobID(out.JobID) {
		return "", &Error{Status: status, Code: "bad_answer", Msg: "POST /generate answered without a usable job_id", Retry: true}
	}
	return out.JobID, nil
}

// validJobID: a job id the generator made up must be neither empty nor long.
func validJobID(id string) bool { return id != "" && len(id) <= maxJobIDLen }

type jobAnswer struct {
	JobID            string          `json:"job_id"`
	Status           string          `json:"status"`
	Result           string          `json:"result"`
	ValidationStatus *string         `json:"validation_status"`
	GeneratorVersion json.RawMessage `json:"generator_version"`
	Files            []FileReport    `json:"files"`
	Model            json.RawMessage `json:"model"`
	AuthoringSVG     *string         `json:"authoring_svg"`
	PreviewPNG       *string         `json:"preview_png_base64"`
	Error            *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Poll asks the generator once about the job (GET /jobs/{id}).
//
//   - done=false, err=nil: the job is still queued or running;
//   - done=false, err!=nil: no answer worth acting on (a network error, a
//     timeout, a 5xx or 429); asking again later may help;
//   - done=true: the outcome. err=nil with the Result of a done job, or the
//     *Error of a failed job, a final HTTP answer, a bad answer, or a 404
//     (Code CodeJobNotFound, the generator lost the job).
func (c *Client) Poll(ctx context.Context, jobID string) (Result, bool, error) {
	if !validJobID(jobID) {
		return Result{}, true, &Error{Code: "bad_answer", Msg: "the job id is empty or too long"}
	}
	status, raw, err := c.call(ctx, c.pollTimeout, http.MethodGet, "/jobs/"+url.PathEscape(jobID), "", nil)
	switch {
	case err != nil:
		// A final error (an answer over the size cap, a missing config) will
		// not change on the next ask.
		return Result{}, !Retryable(err), err
	case status == http.StatusNotFound:
		return Result{}, true, &Error{Status: status, Code: CodeJobNotFound, Msg: "the generator does not know the job " + jobID, Retry: true}
	case status != http.StatusOK:
		e := answerError(status, raw)
		return Result{}, !e.Retry, e
	}
	var a jobAnswer
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{}, true, &Error{Status: status, Code: "bad_answer", Msg: "the job answer is not JSON", Retry: true}
	}
	switch a.Status {
	case "done":
		res, err := a.result(jobID)
		return res, true, err
	case "failed":
		code, msg := "internal_error", "the job failed"
		if a.Error != nil {
			code, msg = a.Error.Code, a.Error.Message
		}
		// memory_limit will fail the same way again; a timeout or a crash
		// may not.
		return Result{}, true, &Error{Status: status, Code: code, Msg: msg, Retry: code != "memory_limit"}
	}
	return Result{}, false, nil
}

// wait polls the job until it is done or ctx ends (Generate).
func (c *Client) wait(ctx context.Context, jobID string) (Result, error) {
	var lastErr error
	for {
		res, done, err := c.Poll(ctx, jobID)
		if done {
			return res, err
		}
		lastErr = err
		t := time.NewTimer(c.pollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			msg := "the job did not finish before the deadline"
			if lastErr != nil {
				msg += ", last error: " + lastErr.Error()
			}
			return Result{}, &Error{Code: "timeout", Msg: msg, Retry: true}
		case <-t.C:
		}
	}
}

func (a jobAnswer) result(jobID string) (Result, error) {
	r := Result{JobID: jobID, Result: a.Result, Files: a.Files}
	if a.ValidationStatus != nil {
		r.ValidationStatus = *a.ValidationStatus
	}
	if len(a.GeneratorVersion) > 0 && string(a.GeneratorVersion) != "null" {
		var buf bytes.Buffer
		if err := json.Compact(&buf, a.GeneratorVersion); err == nil {
			r.GeneratorVersion = buf.String()
		}
	}
	if len(a.Model) > 0 && string(a.Model) != "null" {
		r.Model = a.Model
	}
	if a.AuthoringSVG != nil && *a.AuthoringSVG != "" {
		if len(*a.AuthoringSVG) > MaxSVGBytes {
			r.SVGTooLarge = len(*a.AuthoringSVG)
		} else {
			r.SVG = []byte(*a.AuthoringSVG)
		}
	}
	if a.PreviewPNG != nil && *a.PreviewPNG != "" {
		if enc := *a.PreviewPNG; len(enc) > (MaxPreviewBytes+2)/3*4 {
			// Not decoded: the decoded size is about three quarters of it.
			r.PreviewTooLarge = len(enc) / 4 * 3
		} else {
			png, err := base64.StdEncoding.DecodeString(enc)
			if err != nil {
				return Result{}, &Error{Status: http.StatusOK, Code: "bad_answer", Msg: "preview_png_base64 is not base64", Retry: true}
			}
			if len(png) > MaxPreviewBytes {
				// The encoded length is rounded up, so the exact size is
				// known only after decoding (still bounded by the cap above).
				r.PreviewTooLarge = len(png)
			} else {
				r.PreviewPNG = png
			}
		}
	}
	if r.Result != ResultOK && r.Result != ResultNoAdapter {
		return Result{}, &Error{Status: http.StatusOK, Code: "bad_answer", Msg: fmt.Sprintf("unknown result %q", r.Result)}
	}
	return r, nil
}

// Render answers POST /render: a model in, a validated model and its SVG out
// (the venueplan.Renderer seam). The answers are not applied here: the
// caller puts them into the model first.
func (c *Client) Render(ctx context.Context, in venueplan.RenderInput) (venueplan.RenderOutput, error) {
	if len(in.Model) == 0 {
		return venueplan.RenderOutput{}, &Error{Code: "request.invalid", Msg: "the model is empty"}
	}
	body, err := json.Marshal(map[string]json.RawMessage{"model": in.Model})
	if err != nil {
		return venueplan.RenderOutput{}, &Error{Code: "request.invalid", Msg: err.Error()}
	}
	status, raw, err := c.call(ctx, c.callTimeout, http.MethodPost, "/render", "application/json", body)
	if err != nil {
		return venueplan.RenderOutput{}, err
	}
	if status != http.StatusOK {
		return venueplan.RenderOutput{}, answerError(status, raw)
	}
	var a jobAnswer
	if err := json.Unmarshal(raw, &a); err != nil {
		return venueplan.RenderOutput{}, &Error{Status: status, Code: "bad_answer", Msg: "the render answer is not JSON", Retry: true}
	}
	a.Result = ResultOK
	r, err := a.result("")
	if err != nil {
		return venueplan.RenderOutput{}, err
	}
	if r.SVGTooLarge > 0 {
		return venueplan.RenderOutput{}, &Error{Status: status, Code: "bad_answer", Msg: fmt.Sprintf("the SVG is too large (%d bytes)", r.SVGTooLarge)}
	}
	out := venueplan.RenderOutput{Model: r.Model, SVG: r.SVG, PreviewPNG: r.PreviewPNG, GeneratorVersion: r.GeneratorVersion}
	if len(r.SVG) > 0 {
		g, warnings, planType, verrs := intake.CheckPlanSVG(r.SVG)
		if len(verrs) == 0 {
			if rep, err := json.Marshal(intake.SummaryOf(g, planType, warnings)); err == nil {
				out.Report = rep
			}
		}
	}
	return out, nil
}

// call runs one HTTP request under timeout. A network error or a timeout is
// a retryable *Error.
func (c *Client) call(ctx context.Context, timeout time.Duration, method, path, contentType string, body []byte) (int, []byte, error) {
	if c.base == "" || c.token == "" {
		return 0, nil, &Error{Code: "not_configured", Msg: "VENUE_GEN_URL or VENUE_GEN_TOKEN is empty"}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(cctx, method, c.base+path, rd)
	if err != nil {
		return 0, nil, &Error{Code: "request.invalid", Msg: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		code := "network"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "timeout"
		}
		return 0, nil, &Error{Code: code, Msg: err.Error(), Retry: true}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes+1))
	if err != nil {
		return 0, nil, &Error{Status: resp.StatusCode, Code: "network", Msg: err.Error(), Retry: true}
	}
	if len(raw) > maxAnswerBytes {
		return 0, nil, &Error{Status: resp.StatusCode, Code: "bad_answer", Msg: "the answer is too large"}
	}
	return resp.StatusCode, raw, nil
}

// answerError reads the {"error": {...}} envelope of a non-success answer.
// 5xx (503 queue.full and busy included) and 429 may pass; any other 4xx
// will fail again.
func answerError(status int, raw []byte) *Error {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &env)
	code := env.Error.Code
	if code == "" {
		code = http.StatusText(status)
	}
	return &Error{Status: status, Code: code, Msg: env.Error.Message, Retry: status >= 500 || status == http.StatusTooManyRequests}
}

// quoteName keeps a file name safe inside a Content-Disposition header.
func quoteName(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "").Replace(s)
}
