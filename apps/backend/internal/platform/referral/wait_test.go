package referral

import (
	"testing"
	"time"
)

func TestGateByEvent(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }

	// The session has not ended: wait until its end plus the grace.
	g := GateByEvent(now, SessionEnds{LiveEnd: at(48 * time.Hour), AnyEnd: at(48 * time.Hour)})
	if g.Pay || g.Cancel || g.WaitReason != WaitEvent || !g.Until.Equal(now.Add(48*time.Hour+PayoutGrace)) {
		t.Errorf("future session: %+v", g)
	}
	// Ended, but within the grace: still waiting.
	g = GateByEvent(now, SessionEnds{LiveEnd: at(-time.Hour), AnyEnd: at(-time.Hour)})
	if g.Pay || g.WaitReason != WaitEvent {
		t.Errorf("inside the grace: %+v", g)
	}
	// Ended more than the grace ago: pay.
	if g = GateByEvent(now, SessionEnds{LiveEnd: at(-PayoutGrace - time.Minute)}); !g.Pay {
		t.Errorf("after the grace: %+v", g)
	}
	// Every session cancelled: wait for the friend's refund, then give up.
	g = GateByEvent(now, SessionEnds{AnyEnd: at(-24 * time.Hour)})
	if g.Pay || g.Cancel || g.WaitReason != WaitSessionCancelled || !g.Until.Equal(now.Add(cancelledRecheck)) {
		t.Errorf("cancelled session: %+v", g)
	}
	if g = GateByEvent(now, SessionEnds{AnyEnd: at(-cancelledSessionGiveUp - time.Minute)}); !g.Cancel {
		t.Errorf("cancelled long ago: %+v", g)
	}
	// No session at all: nothing to wait for.
	if g = GateByEvent(now, SessionEnds{}); !g.Pay {
		t.Errorf("no session: %+v", g)
	}
}

func TestGateByBalance(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }

	if v := GateByBalance(now, nil, true, 500, 500); !v.Pay {
		t.Errorf("enough: %+v", v)
	}
	// Enough even after a long wait: pay, never give up.
	if v := GateByBalance(now, ago(40*24*time.Hour), true, 900, 500); !v.Pay {
		t.Errorf("enough after a wait: %+v", v)
	}
	v := GateByBalance(now, nil, true, 499, 500)
	if v.Pay || v.GiveUp || v.WaitReason != WaitBalance || v.Retry != balanceRetryEvery {
		t.Errorf("too little: %+v", v)
	}
	if v.Retry < 3*time.Hour {
		t.Error("the balance must not be read more often than every few hours")
	}
	// A balance that could not be read is not known, not a refusal.
	v = GateByBalance(now, nil, false, 0, 500)
	if v.Pay || v.GiveUp || v.WaitReason != WaitBalanceUnknown {
		t.Errorf("unknown: %+v", v)
	}
	if v = GateByBalance(now, ago(BalanceWaitLimit-time.Minute), true, 0, 500); v.GiveUp {
		t.Errorf("29 days and some: %+v", v)
	}
	if v = GateByBalance(now, ago(BalanceWaitLimit), true, 0, 500); !v.GiveUp {
		t.Errorf("30 days: %+v", v)
	}
}
