// service_actor_writer.go — attributes audit rows to the organization API key
// that made the request (spec §13.1, feature #513 / epic #466 W1-C1b):
//
//	"аудит: все мутации под ключом пишут actor='api_key:<id>'"
//
// Roughly forty call sites across the handler packages build their Event with
// a hardcoded ActorType of "user", because until now every mutating request
// was a user request. Rather than editing each of them (and inviting the next
// one to forget), the attribution is applied once, as a decorator installed in
// wire.go around whatever Writer the server ends up with.
package audit

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// ServiceActorType is the audit_events.actor_type value written for requests
// authenticated with an organization API key. The column is free-form text
// with no CHECK constraint, so no migration is needed.
const ServiceActorType = "api_key"

// ServiceActorPrefix is prepended to the key id to form the composite actor
// label the spec asks for: `api_key:<id>`.
const ServiceActorPrefix = "api_key:"

// serviceActorWriter overrides the actor attribution of every event written
// under an API-key request context.
type serviceActorWriter struct {
	inner Writer
}

// WithServiceActor wraps inner so that events written while an API-key service
// actor is on the context are attributed to that key instead of to whatever
// the call site guessed. User requests pass through byte-for-byte unchanged.
//
// Returns inner unchanged when it is nil, so wire.go can apply the decorator
// unconditionally.
func WithServiceActor(inner Writer) Writer {
	if inner == nil {
		return nil
	}
	return &serviceActorWriter{inner: inner}
}

// Write implements Writer.
func (w *serviceActorWriter) Write(ctx context.Context, ev Event) error {
	return w.inner.Write(ctx, attributeToServiceActor(ctx, ev))
}

// WriteTx implements Writer.
func (w *serviceActorWriter) WriteTx(ctx context.Context, tx pgx.Tx, ev Event) error {
	return w.inner.WriteTx(ctx, tx, attributeToServiceActor(ctx, ev))
}

// attributeToServiceActor rewrites ev's actor fields when ctx carries a
// service actor. It is a pure function of (ctx, ev) so it can be tested
// without a database.
func attributeToServiceActor(ctx context.Context, ev Event) Event {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || !actor.IsService() || actor.ID == "" {
		return ev
	}
	ev.ActorType = ServiceActorType
	ev.ActorID = ServiceActorPrefix + actor.ID
	return ev
}
