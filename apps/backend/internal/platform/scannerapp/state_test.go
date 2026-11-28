package scannerapp

import (
	"errors"
	"testing"
	"time"
)

func TestDeviceStateOf(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	cases := []struct {
		name       string
		kind       string
		until, rev *time.Time
		want       DeviceState
		wantErr    bool
		closedAt   *time.Time
		revoked    bool
	}{
		{name: "standard open", kind: PassKindStandard, until: at(time.Hour), want: StateActive},
		{name: "standard at valid_until", kind: PassKindStandard, until: at(0), want: StateUploadOnly, closedAt: at(0)},
		{name: "standard window ended 23h59m ago", kind: PassKindStandard, until: at(-PassUploadGrace + time.Minute), want: StateUploadOnly, closedAt: at(-PassUploadGrace + time.Minute)},
		{name: "standard window ended 24h ago", kind: PassKindStandard, until: at(-PassUploadGrace), wantErr: true, closedAt: at(-PassUploadGrace)},
		{name: "standard turned off", kind: PassKindStandard, until: at(time.Hour), rev: at(-time.Hour), want: StateUploadOnly, closedAt: at(-time.Hour), revoked: true},
		{name: "turned off after the window ended: the window wins", kind: PassKindStandard, until: at(-2 * time.Hour), rev: at(-time.Hour), want: StateUploadOnly, closedAt: at(-2 * time.Hour)},
		{name: "turned off before the window ended", kind: PassKindStandard, until: at(-time.Hour), rev: at(-2 * time.Hour), want: StateUploadOnly, closedAt: at(-2 * time.Hour), revoked: true},
		{name: "standard without valid_until", kind: PassKindStandard, wantErr: true, closedAt: &time.Time{}},
		{name: "demo open", kind: PassKindDemo, want: StateActive},
		{name: "demo turned off", kind: PassKindDemo, rev: at(-time.Hour), want: StateUploadOnly, closedAt: at(-time.Hour), revoked: true},
		{name: "demo turned off 24h ago", kind: PassKindDemo, rev: at(-PassUploadGrace), wantErr: true, closedAt: at(-PassUploadGrace), revoked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, c, err := DeviceStateOf(tc.kind, tc.until, tc.rev, now)
			if tc.wantErr != (err != nil) || (err != nil && !errors.Is(err, ErrAccessEnded)) {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if !tc.wantErr && state != tc.want {
				t.Fatalf("state = %q, want %q", state, tc.want)
			}
			if tc.closedAt == nil {
				if c != nil {
					t.Fatalf("closure = %+v, want none", c)
				}
				return
			}
			if c == nil || !c.ClosedAt.Equal(*tc.closedAt) || !c.UploadUntil.Equal(c.ClosedAt.Add(PassUploadGrace)) || c.Revoked != tc.revoked {
				t.Fatalf("closure = %+v, want closed at %v revoked %v", c, *tc.closedAt, tc.revoked)
			}
		})
	}
}
