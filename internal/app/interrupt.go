package app

import (
	"context"
	"errors"
	"sync"
)

// errInterrupted is the cancellation cause attached to an active run's context
// when a durably admitted user.interrupt cancels it. It is propagated as the
// cancel cause so the runtime (and its model/tool calls) can observe that a
// deliberate user interrupt — not an unrelated shutdown or deadline — is what
// stopped them.
//
// Classification of the finished run, however, does NOT key on this cause. A
// late interrupt can durably admit in the narrow window after the runtime call
// returns but before the run's completion commits, and context.Cause alone
// cannot linearize that against the completion. Instead the run's canceler token
// carries an explicit finish/interrupt state (see runCanceler) that is resolved
// under the session shard lock, so exactly one of "normal completion" or
// "interrupt" wins. errInterrupted remains the observable cause for the runtime;
// the token state is the source of truth for classification.
var errInterrupted = errors.New("run canceled by user.interrupt")

// runCancelers tracks the cancel function of the single active run per session so
// a durably admitted user.interrupt can cancel it mid-execution. RunStore's
// at-most-one-running-run-per-session invariant means one canceler per session id
// is sufficient; keying by session id also guarantees one session can never
// cancel another's run.
//
// The map is guarded by its own mutex, so register/finish/cancel from multiple
// drain goroutines are safe and never panic. The *linearization* guarantee comes
// from the caller additionally holding the session's shard lock across
// claim+register and finish+classify+Complete (in drainRuns) and across
// admission+cancel (in SendEvent). Because a run's token records whether an
// interrupt claimed it (interrupted) or the run claimed completion first
// (finished), and both transitions happen under that shard lock, the finish-vs-
// interrupt outcome is unambiguous: whichever ran first under the lock wins.
type runCancelers struct {
	mu sync.Mutex
	m  map[string]*runCanceler
}

// runCanceler is the per-run token stored in the registry. Identity comparison of
// the pointer lets finish drop only the exact run it owns, so a finished run can
// never remove a newer run's canceler.
//
// interrupted and finished are the two mutually-racing transitions, both mutated
// only while the session shard lock is held (and under the registry mutex):
//   - cancel sets interrupted (and invokes cancel) when an interrupt is durably
//     admitted before the run claims completion.
//   - finish sets finished when the run reaches its completion commit, and
//     reports whether interrupted was already set.
//
// Serialized by the shard lock, at most one of these observes the other as unset,
// so the run is classified interrupted iff the interrupt won.
type runCanceler struct {
	cancel      context.CancelCauseFunc
	interrupted bool
	finished    bool
}

func newRunCancelers() *runCancelers {
	return &runCancelers{m: map[string]*runCanceler{}}
}

// register records cancel as the active-run canceler for sessionID and returns a
// token to pass back to finish. Given the one-running-run invariant there should
// be no existing entry for the session; if there is (a stale token whose run
// already finished), it is overwritten, which is harmless. Callers invoke this
// under the session shard lock so it is serialized with cancel and finish.
func (r *runCancelers) register(sessionID string, cancel context.CancelCauseFunc) *runCanceler {
	tok := &runCanceler{cancel: cancel}
	r.mu.Lock()
	r.m[sessionID] = tok
	r.mu.Unlock()
	return tok
}

// finish marks tok as having claimed completion and reports whether an interrupt
// had already claimed it. It removes the session's canceler only when it is still
// tok (identity check), so a finished run cannot drop a newer run's registration.
//
// finish MUST be called while holding the session shard lock, so it is serialized
// with SendEvent's admit+cancel under the same lock: if the interrupt's cancel
// ran first, tok.interrupted is already set and finish returns true (classify
// interrupted); if finish runs first, tok.finished is set and the later cancel is
// a no-op (normal completion wins, the interrupt is handled by its own control
// run). Because the whole finish→classify→Complete sequence is under the shard
// lock, no interrupt can admit between classification and the completion commit.
func (r *runCancelers) finish(sessionID string, tok *runCanceler) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	tok.finished = true
	if r.m[sessionID] == tok {
		delete(r.m, sessionID)
	}
	return tok.interrupted
}

// cancel cancels the session's active run (if one is registered and not yet
// finished) with the given cause, and records the interrupt on the token so a
// concurrent finish classifies the run as interrupted. It is an idempotent no-op
// when nothing is registered — an interrupt admitted while the session has no
// active run — or when the active run has already claimed completion (normal
// completion won the race). It cancels only the named session. The stored cancel
// func is invoked outside the registry mutex.
//
// cancel MUST be called while holding the session shard lock (in SendEvent, after
// the interrupt is durably admitted), so it is serialized with finish.
func (r *runCancelers) cancel(sessionID string, cause error) {
	r.mu.Lock()
	tok := r.m[sessionID]
	fire := tok != nil && !tok.finished
	if fire {
		tok.interrupted = true
	}
	r.mu.Unlock()
	if fire {
		tok.cancel(cause)
	}
}
