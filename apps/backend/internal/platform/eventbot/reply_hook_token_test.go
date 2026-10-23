package eventbot

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// failingHTTP is a Telegram HTTP client whose every call fails the way
// net/http does: with a *url.Error that repeats the whole request URL.
type failingHTTP struct{ cause error }

func (f failingHTTP) Do(req *http.Request) (*http.Response, error) {
	return nil, &url.Error{Op: "Post", URL: req.URL.String(), Err: f.cause}
}

// A Bot API URL carries the bot token in its path. The client the Telegram
// library talks through must not hand that URL back inside its errors,
// because the callers log them (getFile, sendMessage, every other call).
func TestSaveBeforeReplyClient_ErrorDoesNotCarryTheToken(t *testing.T) {
	const token = "123456:SECRET-TOKEN-VALUE"
	cause := errors.New("connection reset by peer")
	c := saveBeforeReplyClient{inner: failingHTTP{cause: cause}}

	req, err := http.NewRequest(http.MethodPost, "https://api.telegram.org/bot"+token+"/getFile", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, got := c.Do(req)
	if got == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(got.Error(), token) || strings.Contains(got.Error(), "api.telegram.org") {
		t.Fatalf("the error carries the request URL: %q", got.Error())
	}
	if !errors.Is(got, cause) {
		t.Fatalf("the cause must stay reachable with errors.Is, got %v", got)
	}
}
