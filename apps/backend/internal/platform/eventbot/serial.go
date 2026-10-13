package eventbot

// serial.go — one Telegram account's updates are handled one at a time and in
// the order Telegram delivered them; different accounts run side by side.
//
// The Telegram library reads updates with one worker, but by default it hands
// every update to its own goroutine (`go handler(...)`), so a second message
// or button press of the same person could run while the first was still
// between "read my dialog step" and "write the next one", and read the old
// step. A plain mutex per account would not fix that either: two goroutines
// waiting on one lock take it in no particular order. The bot therefore asks
// the library to call it synchronously (WithNotAsyncHandlers), and
// handleUpdate only puts the update into its sender's lane, in arrival order,
// and returns. Each busy lane has one goroutine that drains it; a lane with
// nothing left is removed, so the map holds only accounts with work in
// flight. A handler that hangs holds up its own account only.

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

// maxQueuedPerUser caps the updates waiting behind a busy handler of one
// account. The rate limits already keep an honest person far below it; it only
// bounds memory when a handler hangs and the person keeps pressing.
const maxQueuedPerUser = 64

// errLaneFull is answered when an account already has maxQueuedPerUser
// updates waiting.
var errLaneFull = errors.New("eventbot: too many updates waiting for this account")

// userSerial runs jobs per Telegram account, one at a time and in submission
// order.
type userSerial struct {
	mu        sync.Mutex
	lanes     map[int64]*userLane
	maxQueued int
	logger    *slog.Logger
}

// userLane is the work waiting for one account. It exists only while a
// goroutine is draining it.
type userLane struct {
	queue []func()
}

func newUserSerial(logger *slog.Logger) *userSerial {
	if logger == nil {
		logger = slog.Default()
	}
	return &userSerial{lanes: map[int64]*userLane{}, maxQueued: maxQueuedPerUser, logger: logger}
}

// submit queues job behind the account's earlier jobs and returns at once.
// It answers errLaneFull, without queuing, when too much is already waiting.
func (s *userSerial) submit(id int64, job func()) error {
	s.mu.Lock()
	if lane, busy := s.lanes[id]; busy {
		if len(lane.queue) >= s.maxQueued {
			s.mu.Unlock()
			return errLaneFull
		}
		lane.queue = append(lane.queue, job)
		s.mu.Unlock()
		return nil
	}
	lane := &userLane{}
	s.lanes[id] = lane
	s.mu.Unlock()
	go s.drain(id, lane, job)
	return nil
}

// drain runs the lane's jobs until none is left, then removes the lane under
// the same lock a submit takes, so a job can never be queued on a lane nobody
// drains any more.
func (s *userSerial) drain(id int64, lane *userLane, job func()) {
	for {
		s.run(id, job)
		s.mu.Lock()
		if len(lane.queue) == 0 {
			delete(s.lanes, id)
			s.mu.Unlock()
			return
		}
		job = lane.queue[0]
		lane.queue[0] = nil
		lane.queue = lane.queue[1:]
		s.mu.Unlock()
	}
}

// run executes one job; a panic is logged and the lane carries on with the
// account's next update.
func (s *userSerial) run(id int64, job func()) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("eventbot: panic in update handler", slog.Int64("telegram_user_id", id),
				slog.Any("panic", r), slog.String("stack", string(debug.Stack())))
		}
	}()
	job()
}

// busy reports how many accounts currently have a lane (tests).
func (s *userSerial) busy() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lanes)
}

// turnKey marks a context as running inside an account's turn.
type turnKey struct{}

// withTurn marks ctx as running inside the turn of account id.
func withTurn(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, turnKey{}, id)
}

// inTurn reports whether ctx already runs inside the turn of account id.
func inTurn(ctx context.Context, id int64) bool {
	cur, ok := ctx.Value(turnKey{}).(int64)
	return ok && cur == id
}

// do runs fn inside the account's turn and waits for it: after the updates
// already queued for the account, and never at the same time as any of them.
// It is how work running outside a handler (a send finishing in the
// background, a sweep) changes a person's dialog state without racing their
// next update. Called from inside that account's own turn it runs fn at once,
// since waiting for the turn it already holds would never end. When ctx ends
// before the turn comes, fn is skipped and ctx's error is returned.
func (s *userSerial) do(ctx context.Context, id int64, fn func(ctx context.Context)) error {
	if inTurn(ctx, id) {
		fn(ctx)
		return nil
	}
	done := make(chan struct{})
	var abandoned atomic.Bool
	err := s.submit(id, func() {
		defer close(done)
		if abandoned.Load() {
			return
		}
		fn(withTurn(ctx, id))
	})
	if err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		abandoned.Store(true)
		return ctx.Err()
	}
}
