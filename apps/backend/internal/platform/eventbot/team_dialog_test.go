package eventbot

import (
	"testing"
	"time"
)

// A dialog that ran out is reported once, to the person it belonged to, and a
// fresh start clears the memory of the lapse.
func TestTeamDialogs_LapseIsReportedOnce(t *testing.T) {
	d := newTeamDialogs()
	const id = int64(42)

	if d.takeLapsed(id) {
		t.Fatal("nothing has lapsed yet")
	}
	d.start(id)
	if _, ok := d.get(id); !ok {
		t.Fatal("a fresh dialog must be live")
	}

	d.mu.Lock()
	dlg := d.byID[id]
	dlg.expires = time.Now().Add(-time.Minute)
	d.byID[id] = dlg
	d.mu.Unlock()

	if _, ok := d.get(id); ok {
		t.Fatal("an expired dialog must not be returned")
	}
	if !d.takeLapsed(id) {
		t.Fatal("the lapse must be remembered")
	}
	if d.takeLapsed(id) {
		t.Fatal("the lapse must be reported once")
	}

	// Someone else's lapse is not ours, and a restart wipes it.
	d.start(7)
	d.mu.Lock()
	d.byID[7] = teamDialog{expires: time.Now().Add(-time.Minute)}
	d.mu.Unlock()
	d.get(7)
	d.start(7)
	if d.takeLapsed(7) {
		t.Fatal("starting a new dialog must clear the lapse")
	}
	if d.takeLapsed(id) {
		t.Fatal("one person's lapse must not leak to another")
	}
}
