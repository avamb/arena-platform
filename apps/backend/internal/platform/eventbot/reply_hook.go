package eventbot

// reply_hook.go — a dialog's next step is in the database BEFORE the person
// sees the question that step asks.
//
// The order matters even though one account's updates are handled one at a
// time (serial.go): a reply sent before the step is written leaves a window in
// which a crash, a deploy or a failed write shows the person a question the
// bot no longer knows it asked, and their answer then lands on the old step.
// A handler that keeps its dialog in a variable and writes it back at the end
// registers that write with withSaveBeforeReply; the bot's Telegram client
// (saveBeforeReplyClient) runs it before every request it sends on that
// context, whichever helper sends it. Any NEW dialog must store its step
// before it replies — explicitly, or through this hook.

import (
	"context"
	"net/http"

	tgbot "github.com/go-telegram/bot"
)

type beforeReplyKey struct{}

// withSaveBeforeReply returns a ctx whose Telegram requests run save first.
// Hooks nest: an inner one runs after the outer ones. save may run several
// times (once per request), so it must be an idempotent write of the current
// state.
func withSaveBeforeReply(ctx context.Context, save func()) context.Context {
	if save == nil {
		return ctx
	}
	if prev, ok := ctx.Value(beforeReplyKey{}).(func()); ok {
		inner := save
		save = func() {
			prev()
			inner()
		}
	}
	return context.WithValue(ctx, beforeReplyKey{}, save)
}

// flushBeforeReply runs the saves registered on ctx.
func flushBeforeReply(ctx context.Context) {
	if save, ok := ctx.Value(beforeReplyKey{}).(func()); ok {
		save()
	}
}

// saveBeforeReplyClient is the HTTP client the Telegram library talks
// through. The library builds each request with the handler's ctx, so the
// saves registered on it run before anything reaches the person.
type saveBeforeReplyClient struct {
	inner tgbot.HttpClient
}

func (c saveBeforeReplyClient) Do(req *http.Request) (*http.Response, error) {
	flushBeforeReply(req.Context())
	resp, err := c.inner.Do(req)
	if err != nil {
		// net/http repeats the whole request URL inside its error, and every
		// Bot API URL carries the bot token in its path (/bot<token>/getFile).
		// The library wraps this error and the callers log it, so the URL is
		// dropped here, once, for every call that goes through this client.
		err = unwrapURLError(err)
	}
	return resp, err
}
