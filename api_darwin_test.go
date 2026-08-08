//go:build darwin

package notify

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// uniqueName gives every test its own notify(3) / distributed name so parallel
// or repeated runs never cross-talk.
func uniqueName(prefix string) string {
	return fmt.Sprintf("com.github.tannevaled.notify.test.%s.%d", prefix, time.Now().UnixNano())
}

// NOTE ON TEST ORDER: the notify(3) delivery test (TestNotifyRoundTrip) is
// declared LAST in this file on purpose. On macOS, initialising libnotify's
// registration machinery can disrupt NSDistributedNotificationCenter mach-port
// delivery established afterwards in the same process (the interaction
// documented on Subscribe). So the distributed-delivery tests are declared
// first (reliable when they run before any notify(3) subscription), and the
// notify(3) delivery test runs last. Do not move TestNotifyRoundTrip above the
// distributed tests.

func TestNotifyValidation(t *testing.T) {
	if _, err := Subscribe("", nil); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("Subscribe(empty) = %v", err)
	}
	if err := Post("bad\x00name"); !errors.Is(err, ErrNameHasNUL) {
		t.Fatalf("Post(nul) = %v", err)
	}
	if _, err := SubscribeDistributed("", nil); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("SubscribeDistributed(empty) = %v", err)
	}
	if err := PostDistributed("ok.name", map[string]string{"": "x"}); !errors.Is(err, ErrBadUserInfo) {
		t.Fatalf("PostDistributed(badUserInfo) = %v", err)
	}
	if err := PostDistributed("bad\x00", nil); !errors.Is(err, ErrNameHasNUL) {
		t.Fatalf("PostDistributed(nul name) = %v", err)
	}
}

func TestSubscribeDistributedNotRunning(t *testing.T) {
	if _, err := SubscribeDistributed(uniqueName("norun"), func(map[string]string) {}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("SubscribeDistributed without Run = %v, want ErrNotRunning", err)
	}
}

// withRun starts Run on a dedicated goroutine, waits for it to become active,
// runs body, then cancels and waits for Run to return. Teardown is registered
// with t.Cleanup so a t.Fatal inside body (which unwinds via runtime.Goexit)
// still cancels the context and joins the Run goroutine — otherwise a failed
// test would leak an active Run and corrupt the ones that follow.
func withRun(t *testing.T, body func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Run returned %v, want context.Canceled", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})

	// Wait until Run is servicing its task queue.
	deadline := time.Now().Add(3 * time.Second)
	for {
		runMu.Lock()
		active := runTasks != nil
		runMu.Unlock()
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not become active within 3s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	body()
}

// TestDistributedRoundTrip proves a real NSDistributedNotificationCenter round
// trip: a subscription attached on the Run thread receives a notification
// posted with a userInfo dictionary, with the payload intact.
func TestDistributedRoundTrip(t *testing.T) {
	if raceEnabled {
		t.Skip("mach-port delivery is unreliable under -race; covered in the CGO=0 lane")
	}
	name := uniqueName("dist")
	got := make(chan map[string]string, 4)

	withRun(t, func() {
		tok, err := SubscribeDistributed(name, func(ui map[string]string) { got <- ui })
		if err != nil {
			t.Fatalf("SubscribeDistributed: %v", err)
		}

		if err := PostDistributed(name, map[string]string{"k": "v", "n": "42"}); err != nil {
			t.Fatalf("PostDistributed: %v", err)
		}

		select {
		case ui := <-got:
			if ui["k"] != "v" || ui["n"] != "42" {
				t.Fatalf("userInfo = %v, want k=v n=42", ui)
			}
			t.Logf("PROOF distributed: received %q userInfo=%v", name, ui)
		case <-time.After(5 * time.Second):
			t.Fatal("distributed handler did not fire within 5s")
		}

		// Cancel removes the last observer for the name (exercises the removeObserver path).
		if err := Cancel(tok); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	})
}

// TestDistributedTwoHandlersSameName covers the multi-handler-per-name and
// non-last-cancel branches.
func TestDistributedTwoHandlersSameName(t *testing.T) {
	if raceEnabled {
		t.Skip("mach-port delivery is unreliable under -race; covered in the CGO=0 lane")
	}
	name := uniqueName("dist2")
	a := make(chan map[string]string, 4)
	b := make(chan map[string]string, 4)

	withRun(t, func() {
		tokA, err := SubscribeDistributed(name, func(ui map[string]string) { a <- ui })
		if err != nil {
			t.Fatalf("SubscribeDistributed A: %v", err)
		}
		tokB, err := SubscribeDistributed(name, func(ui map[string]string) { b <- ui })
		if err != nil {
			t.Fatalf("SubscribeDistributed B: %v", err)
		}

		if err := PostDistributed(name, map[string]string{"x": "1"}); err != nil {
			t.Fatalf("PostDistributed: %v", err)
		}
		for _, ch := range []chan map[string]string{a, b} {
			select {
			case ui := <-ch:
				if ui["x"] != "1" {
					t.Fatalf("userInfo = %v", ui)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("a handler did not fire")
			}
		}

		// Cancel A (non-last): observer stays for B.
		if err := Cancel(tokA); err != nil {
			t.Fatalf("Cancel A: %v", err)
		}
		if err := PostDistributed(name, map[string]string{"x": "2"}); err != nil {
			t.Fatalf("PostDistributed 2: %v", err)
		}
		select {
		case ui := <-b:
			if ui["x"] != "2" {
				t.Fatalf("B second userInfo = %v", ui)
			}
			t.Log("PROOF distributed: B still receives after A cancelled")
		case <-time.After(5 * time.Second):
			t.Fatal("B did not receive after A cancel")
		}
		select {
		case <-a:
			t.Fatal("A received after being cancelled")
		default:
		}
		if err := Cancel(tokB); err != nil {
			t.Fatalf("Cancel B: %v", err)
		}
	})
}

func TestRunAlreadyRunning(t *testing.T) {
	withRun(t, func() {
		if err := Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("second Run = %v, want ErrAlreadyRunning", err)
		}
	})
}

// TestPostUserNotification exercises the user-facing path. A bare test binary
// has no bundle identity, so either ErrNoUserCenter (no center) or nil (center
// present, banner best-effort) is acceptable; the point is it neither panics
// nor shells out.
func TestPostUserNotification(t *testing.T) {
	err := PostUserNotification("notify test", "subtitle", "body text")
	if err != nil && !errors.Is(err, ErrNoUserCenter) {
		t.Fatalf("PostUserNotification = %v, want nil or ErrNoUserCenter", err)
	}
	t.Logf("PostUserNotification returned: %v", err)
}

// TestPostDistributedWithoutRun posts with no active Run (the direct-post
// fallback branch); it must not error.
func TestPostDistributedWithoutRun(t *testing.T) {
	if err := PostDistributed(uniqueName("norunpost"), map[string]string{"k": "v"}); err != nil {
		t.Fatalf("PostDistributed without Run = %v", err)
	}
}

// TestObjcHelpers covers the string/dictionary bridges directly against the
// live Objective-C runtime.
func TestObjcHelpers(t *testing.T) {
	if err := loadFrameworks(); err != nil {
		t.Fatalf("loadFrameworks: %v", err)
	}
	if got := goString(nsString("héllo")); got != "héllo" {
		t.Fatalf("goString round trip = %q", got)
	}
	if got := goString(0); got != "" {
		t.Fatalf("goString(nil) = %q", got)
	}
	if got := goString(nsString("")); got != "" {
		t.Fatalf("goString(empty NSString) = %q", got)
	}
	if got := objcStringify(0); got != "" {
		t.Fatalf("objcStringify(nil) = %q", got)
	}
	// A non-NSString (NSNumber) must degrade via -description, not crash.
	num := class("NSNumber").Send(sel("numberWithInt:"), 7)
	if got := objcStringify(num); got != "7" {
		t.Fatalf("objcStringify(NSNumber 7) = %q, want 7", got)
	}
	// map -> dict -> map round trip.
	in := map[string]string{"a": "1", "b": "two"}
	out := dictToMap(mapToDict(in))
	if len(out) != 2 || out["a"] != "1" || out["b"] != "two" {
		t.Fatalf("dict round trip = %v", out)
	}
	if got := dictToMap(0); len(got) != 0 {
		t.Fatalf("dictToMap(nil) = %v", got)
	}
}

// TestNotifyRoundTrip proves a real notify(3) round trip on this Mac: a custom
// name posted with notify_post is received in-process via the polled
// notify_register_check source, and stops arriving after Cancel. Declared last
// on purpose — see the "NOTE ON TEST ORDER" above.
func TestNotifyRoundTrip(t *testing.T) {
	if raceEnabled {
		t.Skip("notify(3) check-poll delivery is unreliable under -race; covered in the CGO=0 lane")
	}
	name := uniqueName("bus")
	fired := make(chan struct{}, 8)
	tok, err := Subscribe(name, func() { fired <- struct{}{} })
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := Post(name); err != nil {
		t.Fatalf("Post: %v", err)
	}
	select {
	case <-fired:
		t.Logf("PROOF notify(3): handler fired for %q after notify_post", name)
	case <-time.After(3 * time.Second):
		t.Fatal("notify(3) handler did not fire within 3s")
	}

	// After Cancel, a further Post must not reach the handler.
	if err := Cancel(tok); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	drain(fired) // discard any straggler already queued
	if err := Post(name); err != nil {
		t.Fatalf("Post after cancel: %v", err)
	}
	select {
	case <-fired:
		t.Fatal("handler fired after Cancel")
	case <-time.After(3 * PollInterval):
		t.Log("PROOF notify(3): no delivery after Cancel, as expected")
	}
}

func drain(ch chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
