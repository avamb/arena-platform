// client_channel.go — records WHICH client a mutation came through
// (`audit_events.metadata.via`), so an operator reading the audit log can tell
// a change made in the Telegram event-center bot from the same change made in
// admin-web or by a WordPress plugin.
//
// The channel travels in the optional `X-Client-Channel` request header. About
// forty call sites build their own audit.Event, and none of them knows the
// client — so, like service_actor_writer.go, the attribution is applied once:
// an HTTP middleware parks the normalized channel on the request context and
// the WithVia decorator (installed in wire.go) stamps it into the metadata of
// every row written under that context.
//
// The header is self-declared and therefore NOT a security boundary: it is a
// forensic hint next to the authenticated actor, never an input to any
// authorization decision.
package audit

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// MetadataVia is the audit_events.metadata key that names the client channel.
const MetadataVia = "via"

// HeaderClientChannel is the request header clients use to declare their
// channel.
const HeaderClientChannel = "X-Client-Channel"

// Known client channels. Adding one is deliberate: the allowlist keeps the
// metadata values a small, queryable set instead of free text a client (or an
// attacker) controls.
const (
	ChannelTelegramBot = "telegram_bot"
	ChannelAdminWeb    = "admin_web"
	ChannelSitePlugin  = "site_plugin"
)

// NormalizeClientChannel trims and lowercases raw and reports whether it is an
// allowlisted channel. An unknown value is ignored rather than rejected: a
// cosmetic header must never cost a request.
func NormalizeClientChannel(raw string) (string, bool) {
	switch v := strings.ToLower(strings.TrimSpace(raw)); v {
	case ChannelTelegramBot, ChannelAdminWeb, ChannelSitePlugin:
		return v, true
	}
	return "", false
}

type clientChannelKey struct{}

// WithClientChannel returns a context carrying the client channel. An empty
// channel returns ctx unchanged.
func WithClientChannel(ctx context.Context, channel string) context.Context {
	if channel == "" {
		return ctx
	}
	return context.WithValue(ctx, clientChannelKey{}, channel)
}

// ClientChannelFromContext returns the channel stored by WithClientChannel.
func ClientChannelFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(clientChannelKey{}).(string)
	return v, ok && v != ""
}

// viaWriter stamps the request's client channel into every event's metadata.
type viaWriter struct {
	inner Writer
}

// WithVia wraps inner so events written while a client channel is on the
// context carry metadata.via. Returns inner unchanged when it is nil, so
// wire.go can apply the decorator unconditionally.
func WithVia(inner Writer) Writer {
	if inner == nil {
		return nil
	}
	return &viaWriter{inner: inner}
}

// Write implements Writer.
func (w *viaWriter) Write(ctx context.Context, ev Event) error {
	return w.inner.Write(ctx, stampVia(ctx, ev))
}

// WriteTx implements Writer.
func (w *viaWriter) WriteTx(ctx context.Context, tx pgx.Tx, ev Event) error {
	return w.inner.WriteTx(ctx, tx, stampVia(ctx, ev))
}

// stampVia returns ev with metadata.via set from ctx. A "via" the call site
// set itself wins. The metadata map is copied, never mutated: the caller may
// reuse it for another event or read it after the write.
func stampVia(ctx context.Context, ev Event) Event {
	channel, ok := ClientChannelFromContext(ctx)
	if !ok {
		return ev
	}
	if _, exists := ev.Metadata[MetadataVia]; exists {
		return ev
	}
	md := make(map[string]any, len(ev.Metadata)+1)
	for k, v := range ev.Metadata {
		md[k] = v
	}
	md[MetadataVia] = channel
	ev.Metadata = md
	return ev
}
