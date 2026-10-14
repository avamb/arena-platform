package emailstatus

import "testing"

// TestDerive_RegulationLifecycle walks one letter through the table of
// docs/ops/brevo_delivery_status_webhook_ru.md as its events arrive:
// sent, delayed on deferred, delivered, opened; a failure reported after the
// last sign of delivery is failed; a proxy open never counts as reading.
// It adds only the rows TestDerive and its neighbours do not already hold.
func TestDerive_RegulationLifecycle(t *testing.T) {
	steps := []struct {
		name      string
		events    []EventTimes
		want      string
		proxyOnly bool // the set holds a proxy open and no real open
	}{
		{"request", []EventTimes{ev(EventSent, 0, 0)}, StatusSent, false},
		{"deferred", []EventTimes{ev(EventSent, 0, 0), ev(EventDeferred, 1, 1)}, StatusDelayed, false},
		{"delivered after deferral", []EventTimes{ev(EventSent, 0, 0), ev(EventDeferred, 1, 1), ev(EventDelivered, 2, 2)}, StatusDelivered, false},
		{"opened", []EventTimes{ev(EventSent, 0, 0), ev(EventDeferred, 1, 1), ev(EventDelivered, 2, 2), ev(EventOpened, 3, 3)}, StatusOpened, false},
		{"bounce after delivery", []EventTimes{ev(EventSent, 0, 0), ev(EventDelivered, 2, 2), ev(EventHardBounce, 4, 4)}, StatusFailed, false},
		{"proxy open after delivery", []EventTimes{ev(EventSent, 0, 0), ev(EventDelivered, 2, 2), ev(EventProxyOpen, 3, 3)}, StatusDelivered, true},
		{"proxy open before the delivered event", []EventTimes{ev(EventSent, 0, 0), ev(EventProxyOpen, 1, 1), ev(EventDelivered, 2, 2)}, StatusDelivered, true},
		{"proxy open then a real open", []EventTimes{ev(EventDelivered, 1, 1), ev(EventProxyOpen, 2, 2), ev(EventOpened, 5, 5)}, StatusOpened, false},
		{"blocked with nothing else", []EventTimes{ev(EventSent, 0, 0), ev(EventBlocked, 1, 1)}, StatusFailed, false},
	}
	for _, s := range steps {
		got := Derive(s.events)
		if got.Status != s.want {
			t.Errorf("%s: status %q, want %q", s.name, got.Status, s.want)
		}
		if s.proxyOnly {
			if got.OpenedAt != nil {
				t.Errorf("%s: a proxy open must not set OpenedAt (%v)", s.name, got.OpenedAt)
			}
			if got.ProxyOpenedAt == nil {
				t.Errorf("%s: a proxy open must set ProxyOpenedAt", s.name)
			}
		}
	}
}
