package generator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
)

const testToken = "gen-test-token"

// stub is a fake generator: submit answers 202 with a new job id, the job
// answers what the test sets.
type stub struct {
	mu        sync.Mutex
	submits   int
	polls     int
	kinds     []string
	names     []string
	authOK    bool
	submitFn  func(n int) (int, string)
	jobFn     func(id string, poll int) (int, string)
	renderFn  func(body []byte) (int, string)
	lastModel string
}

func (s *stub) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.authOK = r.Header.Get("Authorization") == "Bearer "+testToken
		if !s.authOK {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"auth.required","message":"no"}}`)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/generate":
			s.submits++
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				t.Errorf("multipart: %v", err)
			}
			s.kinds = append(s.kinds, r.FormValue("kinds"))
			for _, fh := range r.MultipartForm.File["files"] {
				s.names = append(s.names, fh.Filename)
			}
			status, body := http.StatusAccepted, `{"job_id":"job-`+string(rune('0'+s.submits))+`","status":"queued"}`
			if s.submitFn != nil {
				status, body = s.submitFn(s.submits)
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/jobs/"):
			s.polls++
			status, body := s.jobFn(strings.TrimPrefix(r.URL.Path, "/jobs/"), s.polls)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		case r.Method == http.MethodPost && r.URL.Path == "/render":
			raw, _ := io.ReadAll(r.Body)
			s.lastModel = string(raw)
			status, body := s.renderFn(raw)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func newClient(t *testing.T, s *stub, opts ...Option) *Client {
	t.Helper()
	ts := httptest.NewServer(s.handler(t))
	t.Cleanup(ts.Close)
	base := []Option{WithPollInterval(5 * time.Millisecond), WithDeadline(5 * time.Second)}
	return New(ts.URL+"/", testToken, append(base, opts...)...)
}

const doneOK = `{"job_id":"job-1","status":"done","result":"ok","validation_status":"PASS",
 "generator_version":{"service":"g2b-0.2.0","commit":"abc"},
 "files":[{"index":0,"declared_kind":"xlsx","detected_kind":"xlsx","sha256":"x","size_bytes":1,"adapter":"xlsx_seating_adapter","outcome":"used","reason":null,"seat_count":3,"validation_status":"PASS"}],
 "model":{"schema":"seating-json-v2"},"authoring_svg":"<svg/>","preview_png_base64":"` + "iVBORw==" + `"}`

var files = []File{{Name: "hall.xlsx", Kind: KindXLSX, Data: []byte("PK..")}, {Name: `we"ird.pdf`, Kind: KindPDF, Data: []byte("%PDF")}}

func TestGenerate_OK(t *testing.T) {
	s := &stub{jobFn: func(_ string, poll int) (int, string) {
		if poll < 3 {
			return 200, `{"job_id":"job-1","status":"running"}`
		}
		return 200, doneOK
	}}
	res, err := newClient(t, s).Generate(context.Background(), files)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !s.authOK {
		t.Error("the bearer token was not sent")
	}
	if res.Result != ResultOK || res.ValidationStatus != StatusPass || string(res.SVG) != "<svg/>" ||
		len(res.Files) != 1 || res.Files[0].Outcome != "used" || string(res.Model) != `{"schema":"seating-json-v2"}` {
		t.Fatalf("result %+v", res)
	}
	if want, _ := base64.StdEncoding.DecodeString("iVBORw=="); string(res.PreviewPNG) != string(want) {
		t.Errorf("preview %v", res.PreviewPNG)
	}
	if res.GeneratorVersion != `{"service":"g2b-0.2.0","commit":"abc"}` {
		t.Errorf("generator version %q", res.GeneratorVersion)
	}
	if s.kinds[0] != `["xlsx","pdf"]` || len(s.names) != 2 || s.names[0] != "hall.xlsx" {
		t.Errorf("sent kinds %v names %v", s.kinds, s.names)
	}
	if s.polls != 3 {
		t.Errorf("polls %d, want 3", s.polls)
	}
}

func TestGenerate_NoAdapterAndNullSVG(t *testing.T) {
	s := &stub{jobFn: func(string, int) (int, string) {
		return 200, `{"job_id":"job-1","status":"done","result":"no_adapter","validation_status":null,"generator_version":{},
		 "files":[{"index":0,"outcome":"no_adapter","reason":"raster_profile_required"}],"model":null,"authoring_svg":null,"preview_png_base64":null}`
	}}
	res, err := newClient(t, s).Generate(context.Background(), files[:1])
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.Result != ResultNoAdapter || res.ValidationStatus != "" || res.SVG != nil || res.Model != nil || res.PreviewPNG != nil {
		t.Fatalf("result %+v", res)
	}
}

func wantErr(t *testing.T, err error, code string, retry bool) {
	t.Helper()
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("error %v is not *Error", err)
	}
	if ge.Code != code || ge.Retry != retry || Retryable(err) != retry {
		t.Fatalf("error %+v, want code %s retry %v", ge, code, retry)
	}
}

func TestGenerate_FailedJob(t *testing.T) {
	for _, tc := range []struct {
		code  string
		retry bool
	}{{"memory_limit", false}, {"timeout", true}, {"worker_crashed", true}, {"internal_error", true}} {
		s := &stub{jobFn: func(string, int) (int, string) {
			return 200, `{"job_id":"job-1","status":"failed","error":{"code":"` + tc.code + `","message":"x"}}`
		}}
		_, err := newClient(t, s).Generate(context.Background(), files[:1])
		wantErr(t, err, tc.code, tc.retry)
	}
}

func TestGenerate_SubmitErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		retry  bool
	}{
		{503, "queue.full", true}, {500, "internal_error", true}, {400, "kinds.invalid", false},
		{413, "file.too_large", false}, {401, "auth.required", false},
	} {
		s := &stub{submitFn: func(int) (int, string) {
			return tc.status, `{"error":{"code":"` + tc.code + `","message":"m"}}`
		}}
		_, err := newClient(t, s).Generate(context.Background(), files[:1])
		wantErr(t, err, tc.code, tc.retry)
	}
}

func TestGenerate_WrongTokenIsFinal(t *testing.T) {
	s := &stub{}
	ts := httptest.NewServer(s.handler(t))
	t.Cleanup(ts.Close)
	_, err := New(ts.URL, "wrong").Generate(context.Background(), files[:1])
	wantErr(t, err, "auth.required", false)
}

func TestGenerate_LostJobIsSentAgainOnce(t *testing.T) {
	s := &stub{jobFn: func(id string, _ int) (int, string) {
		if id == "job-1" {
			return 404, `{"error":{"code":"job.not_found","message":"gone"}}`
		}
		return 200, doneOK
	}}
	res, err := newClient(t, s).Generate(context.Background(), files[:1])
	if err != nil || res.Result != ResultOK {
		t.Fatalf("Generate: %v %+v", err, res)
	}
	if s.submits != 2 {
		t.Errorf("submits %d, want 2", s.submits)
	}

	// Lost twice: a retryable error, not a third submit.
	s2 := &stub{jobFn: func(string, int) (int, string) {
		return 404, `{"error":{"code":"job.not_found","message":"gone"}}`
	}}
	_, err = newClient(t, s2).Generate(context.Background(), files[:1])
	wantErr(t, err, "job.not_found", true)
	if s2.submits != 2 {
		t.Errorf("submits %d, want 2", s2.submits)
	}
}

func TestGenerate_Deadline(t *testing.T) {
	s := &stub{jobFn: func(string, int) (int, string) { return 200, `{"status":"running"}` }}
	start := time.Now()
	_, err := newClient(t, s, WithDeadline(80*time.Millisecond)).Generate(context.Background(), files[:1])
	wantErr(t, err, "timeout", true)
	if time.Since(start) > 2*time.Second {
		t.Errorf("the deadline was not kept: %v", time.Since(start))
	}
}

func TestGenerate_CallTimeout(t *testing.T) {
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-block
	}))
	t.Cleanup(func() { close(block); ts.Close() })
	_, err := New(ts.URL, testToken, WithCallTimeout(50*time.Millisecond)).Generate(context.Background(), files[:1])
	wantErr(t, err, "timeout", true)
}

func TestGenerate_NetworkErrorIsRetry(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	_, err := New(url, testToken).Generate(context.Background(), files[:1])
	var ge *Error
	if !errors.As(err, &ge) || !ge.Retry {
		t.Fatalf("a closed server must be a retryable error, got %v", err)
	}
}

func TestGenerate_OneCallAtATime(t *testing.T) {
	var running, peak int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/generate" {
			n := atomic.AddInt32(&running, 1)
			for {
				p := atomic.LoadInt32(&peak)
				if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
					break
				}
			}
			w.WriteHeader(202)
			_, _ = io.WriteString(w, `{"job_id":"j"}`)
			return
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		_, _ = io.WriteString(w, doneOK)
	}))
	t.Cleanup(ts.Close)
	c := New(ts.URL, testToken, WithPollInterval(time.Millisecond))
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Generate(context.Background(), files[:1]); err != nil {
				t.Errorf("Generate: %v", err)
			}
		}()
	}
	wg.Wait()
	if peak != 1 {
		t.Errorf("%d generator jobs ran at once, want 1", peak)
	}
}

func TestGenerate_RefusesBadFileCount(t *testing.T) {
	c := New("http://x", testToken)
	if _, err := c.Generate(context.Background(), nil); err == nil || Retryable(err) {
		t.Errorf("no files: %v", err)
	}
	many := make([]File, MaxFiles+1)
	if _, err := c.Generate(context.Background(), many); err == nil || Retryable(err) {
		t.Errorf("too many files: %v", err)
	}
}

// TestSubmitAndPoll: the two short calls of the worker job.
func TestSubmitAndPoll(t *testing.T) {
	answers := map[string][2]any{
		"job-run":    {200, `{"job_id":"job-run","status":"running"}`},
		"job-busy":   {503, `{"error":{"code":"busy","message":"secret text"}}`},
		"job-lost":   {404, `{"error":{"code":"job.not_found","message":"gone"}}`},
		"job-failed": {200, `{"job_id":"job-failed","status":"failed","error":{"code":"memory_limit","message":"x"}}`},
		"job-final":  {400, `{"error":{"code":"bad","message":"x"}}`},
		"job-done":   {200, doneOK},
	}
	s := &stub{jobFn: func(id string, _ int) (int, string) {
		a := answers[id]
		return a[0].(int), a[1].(string)
	}}
	c := newClient(t, s)
	id, err := c.Submit(context.Background(), files[:1])
	if err != nil || id != "job-1" || s.submits != 1 || s.polls != 0 {
		t.Fatalf("Submit: %q %v, submits %d polls %d", id, err, s.submits, s.polls)
	}
	if _, err := c.Submit(context.Background(), nil); err == nil || Retryable(err) {
		t.Errorf("Submit without files: %v", err)
	}

	if _, done, err := c.Poll(context.Background(), "job-run"); done || err != nil {
		t.Errorf("running: done %v err %v", done, err)
	}
	_, done, err := c.Poll(context.Background(), "job-busy")
	if done {
		t.Error("a 503 is not an outcome")
	}
	wantErr(t, err, "busy", true)
	_, done, err = c.Poll(context.Background(), "job-lost")
	if !done || !IsJobLost(err) {
		t.Errorf("404: done %v err %v", done, err)
	}
	_, done, err = c.Poll(context.Background(), "job-failed")
	if !done {
		t.Error("a failed job is an outcome")
	}
	wantErr(t, err, "memory_limit", false)
	_, done, err = c.Poll(context.Background(), "job-final")
	if !done {
		t.Error("a final 4xx is an outcome")
	}
	wantErr(t, err, "bad", false)
	res, done, err := c.Poll(context.Background(), "job-done")
	if !done || err != nil || res.Result != ResultOK || res.JobID != "job-done" {
		t.Errorf("done: %v %v %+v", done, err, res)
	}
	if s.polls != 6 {
		t.Errorf("polls %d, want one GET per Poll", s.polls)
	}
}

func TestPoll_Timeout(t *testing.T) {
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	t.Cleanup(func() { close(block); ts.Close() })
	start := time.Now()
	_, done, err := New(ts.URL, testToken, WithPollTimeout(50*time.Millisecond)).Poll(context.Background(), "j")
	if done {
		t.Error("a timeout is not an outcome")
	}
	wantErr(t, err, "timeout", true)
	if time.Since(start) > 2*time.Second {
		t.Errorf("the poll timeout was not kept: %v", time.Since(start))
	}
}

func TestRender(t *testing.T) {
	s := &stub{renderFn: func([]byte) (int, string) {
		return 200, `{"model":{"m":2},"validation_status":"PASS","authoring_svg":"<svg/>","svg_info":{},"preview_png_base64":null,"generator_version":{"service":"g"}}`
	}}
	var r venueplan.Renderer = newClient(t, s)
	out, err := r.Render(context.Background(), venueplan.RenderInput{Model: json.RawMessage(`{"m":1}`)})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(out.Model) != `{"m":2}` || string(out.SVG) != "<svg/>" || out.GeneratorVersion != `{"service":"g"}` {
		t.Errorf("out %+v", out)
	}
	if s.lastModel != `{"model":{"m":1}}` {
		t.Errorf("sent %s", s.lastModel)
	}
	s.renderFn = func([]byte) (int, string) { return 503, `{"error":{"code":"busy","message":"try again"}}` }
	_, err = r.Render(context.Background(), venueplan.RenderInput{Model: json.RawMessage(`{}`)})
	wantErr(t, err, "busy", true)
	s.renderFn = func([]byte) (int, string) { return 400, `{"error":{"code":"model.invalid","message":"bad"}}` }
	_, err = r.Render(context.Background(), venueplan.RenderInput{Model: json.RawMessage(`{}`)})
	wantErr(t, err, "model.invalid", false)
}

// doneWith is a done job with the given SVG and preview (base64) strings.
func doneWith(svg, previewB64 string) string {
	return `{"job_id":"job-1","status":"done","result":"ok","validation_status":"PASS","generator_version":{},"files":[],` +
		`"model":{"schema":"seating-json-v2"},"authoring_svg":"` + svg + `","preview_png_base64":"` + previewB64 + `"}`
}

func TestPoll_SVGOverTheLimitIsDroppedNotCopied(t *testing.T) {
	big := strings.Repeat("a", MaxSVGBytes+1)
	s := &stub{jobFn: func(string, int) (int, string) { return 200, doneWith(big, "iVBORw==") }}
	res, done, err := newClient(t, s).Poll(context.Background(), "job-1")
	if !done || err != nil {
		t.Fatalf("done %v err %v", done, err)
	}
	if res.SVG != nil || res.SVGTooLarge != MaxSVGBytes+1 {
		t.Errorf("SVG len %d, SVGTooLarge %d", len(res.SVG), res.SVGTooLarge)
	}
	if len(res.PreviewPNG) == 0 {
		t.Error("a small preview was lost with the big SVG")
	}
	// Exactly at the limit is taken.
	at := strings.Repeat("a", MaxSVGBytes)
	s2 := &stub{jobFn: func(string, int) (int, string) { return 200, doneWith(at, "") }}
	res, _, err = newClient(t, s2).Poll(context.Background(), "job-1")
	if err != nil || len(res.SVG) != MaxSVGBytes || res.SVGTooLarge != 0 {
		t.Errorf("at the limit: err %v len %d too large %d", err, len(res.SVG), res.SVGTooLarge)
	}
}

func TestPoll_PreviewOverTheLimitKeepsTheSVG(t *testing.T) {
	big := base64.StdEncoding.EncodeToString(make([]byte, MaxPreviewBytes+1))
	s := &stub{jobFn: func(string, int) (int, string) { return 200, doneWith("<svg/>", big) }}
	res, done, err := newClient(t, s).Poll(context.Background(), "job-1")
	if !done || err != nil {
		t.Fatalf("done %v err %v", done, err)
	}
	if string(res.SVG) != "<svg/>" || res.PreviewPNG != nil || res.PreviewTooLarge < MaxPreviewBytes {
		t.Errorf("svg %q preview %d too large %d", res.SVG, len(res.PreviewPNG), res.PreviewTooLarge)
	}
	ok := base64.StdEncoding.EncodeToString(make([]byte, MaxPreviewBytes))
	s2 := &stub{jobFn: func(string, int) (int, string) { return 200, doneWith("<svg/>", ok) }}
	res, _, err = newClient(t, s2).Poll(context.Background(), "job-1")
	if err != nil || len(res.PreviewPNG) != MaxPreviewBytes || res.PreviewTooLarge != 0 {
		t.Errorf("at the limit: err %v len %d", err, len(res.PreviewPNG))
	}
}

func TestRender_SVGOverTheLimitIsRefused(t *testing.T) {
	big := strings.Repeat("a", MaxSVGBytes+1)
	s := &stub{renderFn: func([]byte) (int, string) { return 200, doneWith(big, "") }}
	_, err := newClient(t, s).Render(context.Background(), venueplan.RenderInput{Model: json.RawMessage(`{"a":1}`)})
	wantErr(t, err, "bad_answer", false)
}

func TestCall_AnswerBodyCap(t *testing.T) {
	if maxAnswerBytes >= 64<<20 || maxAnswerBytes < 2*MaxSVGBytes+MaxPreviewBytes*4/3 {
		t.Fatalf("maxAnswerBytes %d does not fit its parts", maxAnswerBytes)
	}
	s := &stub{jobFn: func(string, int) (int, string) { return 200, strings.Repeat("x", maxAnswerBytes+1) }}
	_, done, err := newClient(t, s).Poll(context.Background(), "job-1")
	if !done {
		t.Error("an oversized answer is final")
	}
	wantErr(t, err, "bad_answer", false)
}

func TestPoll_JobIDIsEscapedAndChecked(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"status":"running"}`)
	}))
	t.Cleanup(ts.Close)
	c := New(ts.URL, testToken)
	if _, done, err := c.Poll(context.Background(), "a/b c?x=1"); done || err != nil {
		t.Fatalf("done %v err %v", done, err)
	}
	if gotPath != "/jobs/a%2Fb%20c%3Fx=1" {
		t.Errorf("path %q", gotPath)
	}
	gotPath = ""
	for _, id := range []string{"", strings.Repeat("j", 201)} {
		_, done, err := c.Poll(context.Background(), id)
		if !done {
			t.Errorf("id of %d chars: not final", len(id))
		}
		wantErr(t, err, "bad_answer", false)
	}
	if gotPath != "" {
		t.Errorf("a refused id reached the generator: %q", gotPath)
	}
	if _, done, err := c.Poll(context.Background(), strings.Repeat("j", 200)); done || err != nil {
		t.Errorf("200 chars: done %v err %v", done, err)
	}
}

func TestSubmit_RefusesAnUnusableJobID(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("j", 201)} {
		body := `{"job_id":"` + id + `"}`
		s := &stub{submitFn: func(int) (int, string) { return 202, body }}
		_, err := newClient(t, s).Submit(context.Background(), files[:1])
		wantErr(t, err, "bad_answer", true)
	}
}
