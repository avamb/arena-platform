// Package generator is the HTTP client of the venue SVG generator
// (apps/venue-svg-generator, contract HALL_MODEL.md section 7). Only
// arena-worker uses it (spec 64 wave 2): the job venue_plan.generate sends the
// files of an intake with Generate, and Render is the venueplan.Renderer seam
// for the answers loop of a later wave.
//
// Generate submits the files (POST /generate, multipart) and polls the job
// (GET /jobs/{id}) until it is done or failed. One Client lets one Generate run
// at a time: the generator itself runs one job at a time, and a worker process
// must not pile its jobs into the generator's queue. Every HTTP call has its
// own timeout (CallTimeout); the whole submit and poll has a deadline
// (Deadline) that stays under the worker's stale-claim timeout (5 minutes).
//
// Errors are *Error: Retry tells the caller whether a later attempt may
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
	DefaultDeadline     = 4 * time.Minute
	DefaultPollInterval = 2 * time.Second
	maxAnswerBytes      = 64 << 20
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
	deadline     time.Duration
	pollInterval time.Duration
	sem          chan struct{}
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client (tests).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithCallTimeout sets the timeout of one HTTP call.
func WithCallTimeout(d time.Duration) Option { return func(c *Client) { c.callTimeout = d } }

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
func (c *Client) Generate(ctx context.Context, files []File) (Result, error) {
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
		jobID, err := c.submit(ctx, files)
		if err != nil {
			return Result{}, err
		}
		res, err := c.poll(ctx, jobID)
		var ge *Error
		if errors.As(err, &ge) && ge.Code == "job.not_found" && !resent {
			resent = true
			continue
		}
		return res, err
	}
}

func (c *Client) submit(ctx context.Context, files []File) (string, error) {
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
	if err := mw.Close(); err != nil {
		return "", &Error{Code: "request.invalid", Msg: err.Error()}
	}
	status, raw, err := c.call(ctx, http.MethodPost, "/generate", mw.FormDataContentType(), body.Bytes())
	if err != nil {
		return "", err
	}
	if status != http.StatusAccepted && status != http.StatusOK {
		return "", answerError(status, raw)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.JobID == "" {
		return "", &Error{Status: status, Code: "bad_answer", Msg: "POST /generate answered without a job_id", Retry: true}
	}
	return out.JobID, nil
}

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

func (c *Client) poll(ctx context.Context, jobID string) (Result, error) {
	var lastErr error
	for {
		status, raw, err := c.call(ctx, http.MethodGet, "/jobs/"+jobID, "", nil)
		switch {
		case err != nil:
			lastErr = err
		case status == http.StatusNotFound:
			return Result{}, &Error{Status: status, Code: "job.not_found", Msg: "the generator does not know the job " + jobID, Retry: true}
		case status != http.StatusOK:
			e := answerError(status, raw)
			if !e.Retry {
				return Result{}, e
			}
			lastErr = e
		default:
			var a jobAnswer
			if err := json.Unmarshal(raw, &a); err != nil {
				return Result{}, &Error{Status: status, Code: "bad_answer", Msg: "the job answer is not JSON", Retry: true}
			}
			switch a.Status {
			case "done":
				return a.result(jobID)
			case "failed":
				code, msg := "internal_error", "the job failed"
				if a.Error != nil {
					code, msg = a.Error.Code, a.Error.Message
				}
				// memory_limit will fail the same way again; a timeout or a
				// crash may not.
				return Result{}, &Error{Status: status, Code: code, Msg: msg, Retry: code != "memory_limit"}
			}
			lastErr = nil
		}
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
		r.SVG = []byte(*a.AuthoringSVG)
	}
	if a.PreviewPNG != nil && *a.PreviewPNG != "" {
		png, err := base64.StdEncoding.DecodeString(*a.PreviewPNG)
		if err != nil {
			return Result{}, &Error{Status: http.StatusOK, Code: "bad_answer", Msg: "preview_png_base64 is not base64", Retry: true}
		}
		r.PreviewPNG = png
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
	status, raw, err := c.call(ctx, http.MethodPost, "/render", "application/json", body)
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

// call runs one HTTP request under the call timeout. A network error or a
// timeout is a retryable *Error.
func (c *Client) call(ctx context.Context, method, path, contentType string, body []byte) (int, []byte, error) {
	if c.base == "" || c.token == "" {
		return 0, nil, &Error{Code: "not_configured", Msg: "VENUE_GEN_URL or VENUE_GEN_TOKEN is empty"}
	}
	cctx, cancel := context.WithTimeout(ctx, c.callTimeout)
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
