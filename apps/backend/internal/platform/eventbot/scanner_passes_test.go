package eventbot

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCanScannerPasses(t *testing.T) {
	org := uuid.New()
	mk := func(role string, super bool, own map[uuid.UUID]string) *Identity {
		return &Identity{Current: &Membership{OrgID: org, Role: role}, Superadmin: super, OwnRoles: own}
	}
	cases := []struct {
		name string
		id   *Identity
		want bool
	}{
		{"owner", mk(membershipRoleOwner, false, nil), true},
		{"manager", mk(membershipRoleManager, false, nil), true},
		{"agent", mk("agent", false, nil), false},
		{"superadmin without membership", mk(membershipRoleOwner, true, map[uuid.UUID]string{}), false},
		{"superadmin owner of the organization", mk(membershipRoleOwner, true, map[uuid.UUID]string{org: membershipRoleOwner}), true},
		{"superadmin agent of the organization", mk(membershipRoleOwner, true, map[uuid.UUID]string{org: "agent"}), false},
		{"nil", nil, false},
		{"no organization", &Identity{}, false},
	}
	for _, c := range cases {
		if got := canScannerPasses(c.id); got != c.want {
			t.Errorf("%s: canScannerPasses = %v, want %v", c.name, got, c.want)
		}
	}
}

// The wizard's presses of one person run one at a time (spWizardPress holds
// spLocks for the person): a double tap on "sp:d:<n>" cannot run two creates.
func TestScannerPassWizardLockSerializes(t *testing.T) {
	var b Bot
	key := uuid.NewSHA1(uuid.NameSpaceOID, []byte("sp:42"))
	var inside, maxInside int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := b.spLocks.lock(key)
			defer unlock()
			n := atomic.AddInt32(&inside, 1)
			for {
				m := atomic.LoadInt32(&maxInside)
				if n <= m || atomic.CompareAndSwapInt32(&maxInside, m, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&inside, -1)
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Fatalf("two presses ran together: %d", maxInside)
	}
}
