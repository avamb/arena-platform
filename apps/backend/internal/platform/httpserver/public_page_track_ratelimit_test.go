package httpserver

import "testing"

// The page statistics beacon has its own two buckets (the page, the client IP).
// Both are counted on every call, never short-circuited, and 0 switches one off.

func TestCheckPageTrack_PageBucket(t *testing.T) {
	rl := newPublicFeedRateLimiter(0, 0, 0)
	rl.pageTrackEventLimit, rl.pageTrackIPLimit = 3, 0
	for i := 0; i < 3; i++ {
		if ok, _ := rl.CheckPageTrack("acme/show", "10.0.0.1"); !ok {
			t.Fatalf("call %d blocked", i+1)
		}
	}
	ok, retry := rl.CheckPageTrack("acme/show", "10.0.0.2") // another address, same page
	if ok || retry < 1 || retry > 60 {
		t.Fatalf("4th call: ok=%v retry=%d, want blocked with 1..60", ok, retry)
	}
	if ok, _ := rl.CheckPageTrack("acme/other", "10.0.0.2"); !ok {
		t.Fatal("another page must have its own bucket")
	}
}

func TestCheckPageTrack_IPBucketAndIPv6Prefix(t *testing.T) {
	rl := newPublicFeedRateLimiter(0, 0, 0)
	rl.pageTrackEventLimit, rl.pageTrackIPLimit = 0, 2
	rl.CheckPageTrack("a/b", "10.0.0.1")
	rl.CheckPageTrack("c/d", "10.0.0.1")
	if ok, _ := rl.CheckPageTrack("e/f", "10.0.0.1"); ok {
		t.Fatal("the same address on other pages must share one bucket")
	}
	if ok, _ := rl.CheckPageTrack("e/f", "10.0.0.9"); !ok {
		t.Fatal("another address must not be blocked")
	}
	// Two addresses of one /64 are one client.
	rl.CheckPageTrack("a/b", "2001:db8:1:2::1")
	rl.CheckPageTrack("a/b", "2001:db8:1:2::2")
	if ok, _ := rl.CheckPageTrack("a/b", "2001:db8:1:2::3"); ok {
		t.Fatal("addresses of one IPv6 /64 must share a bucket")
	}
}

func TestCheckPageTrack_BothBucketsAreCountedEvenWhenOneBlocks(t *testing.T) {
	rl := newPublicFeedRateLimiter(0, 0, 0)
	rl.pageTrackEventLimit, rl.pageTrackIPLimit = 1, 3
	rl.CheckPageTrack("a/b", "10.0.0.1")
	for i := 0; i < 2; i++ { // blocked by the page bucket; the IP bucket must still count
		if ok, _ := rl.CheckPageTrack("a/b", "10.0.0.1"); ok {
			t.Fatal("page bucket should block")
		}
	}
	// A different page, same address: the IP bucket (limit 3) is already used up.
	if ok, _ := rl.CheckPageTrack("c/d", "10.0.0.1"); ok {
		t.Fatal("the IP bucket was not counted while the page bucket blocked")
	}
}

func TestCheckPageTrack_ZeroDisables(t *testing.T) {
	rl := newPublicFeedRateLimiter(0, 0, 0)
	rl.pageTrackEventLimit, rl.pageTrackIPLimit = 0, 0
	for i := 0; i < 1000; i++ {
		if ok, _ := rl.CheckPageTrack("a/b", "10.0.0.1"); !ok {
			t.Fatal("a disabled check blocked")
		}
	}
}
