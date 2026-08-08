//go:build darwin

package notify

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/ebitengine/purego/objc"
)

// PollInterval is how often a notify(3) [Subscribe] polls notify_check for its
// name. It bounds delivery latency for the notify(3) bus and the per-interval
// coalescing window. Set it before calling Subscribe. It does not affect
// distributed subscriptions, which are event-driven.
var PollInterval = 50 * time.Millisecond

// ---------------------------------------------------------------------------
// notify(3) event bus: Subscribe / Post (Cancel is the shared one in notify.go)
// ---------------------------------------------------------------------------

// Subscribe registers handler for the Darwin notify(3) name and returns a
// token to [Cancel] it with. Each time the name is posted (by this or any other
// process) handler is called on a background goroutine dedicated to the
// subscription. No [Run] is required for notify(3) delivery.
//
// Delivery is edge-detected by polling notify_check every [PollInterval], so a
// handler fires within one interval of a post, and multiple posts within one
// interval coalesce into a single call (matching notify(3)'s own coalescing).
func Subscribe(name string, handler Handler) (Token, error) {
	if err := validateName(name); err != nil {
		return 0, err
	}
	if err := loadFrameworks(); err != nil {
		return 0, err
	}
	var ntoken int32
	if st := cNotifyRegisterCheck(name, &ntoken); st != notifyStatusOK {
		return 0, fmt.Errorf("%w: notify_register_check(%q) status %d", ErrRegister, name, st)
	}
	// Consume the initial check state so the first real post — not registration —
	// is what fires the handler.
	var initial int32
	cNotifyCheck(ntoken, &initial)

	stop := make(chan struct{})
	done := make(chan struct{})
	interval := PollInterval
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				var c int32
				if cNotifyCheck(ntoken, &c) == notifyStatusOK && c == 1 {
					handler()
				}
			}
		}
	}()

	tok := reg.add(func() {
		close(stop)
		<-done // ensure the poller has stopped touching ntoken before cancelling
		cNotifyCancel(ntoken)
	})
	return tok, nil
}

// Post fires the Darwin notify(3) name to every subscriber in the system.
func Post(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := loadFrameworks(); err != nil {
		return err
	}
	if st := cNotifyPost(name); st != notifyStatusOK {
		return fmt.Errorf("%w: notify_post(%q) status %d", ErrPost, name, st)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Run loop: a single dedicated thread that owns every Objective-C call made on
// behalf of distributed subscriptions, plus the Foundation run loop that
// delivers them.
// ---------------------------------------------------------------------------

// pumpSeconds is how long each run-loop turn blocks waiting for delivery before
// the loop checks for cancellation and queued work. It bounds task latency.
const pumpSeconds = 0.05

var (
	runMu    sync.Mutex
	runTasks chan func() // non-nil exactly while Run is active
	runObj   objc.ID     // GoNotifyRunner instance, valid on the run thread
)

// runnerClassOnce registers the internal Objective-C runner class exactly once.
var (
	runnerClassOnce sync.Once
	runnerClass     objc.Class
	runnerClassErr  error
)

func doRegisterRunnerClass() (objc.Class, error) {
	runnerClassOnce.Do(func() {
		runnerClass, runnerClassErr = objc.RegisterClass(
			"GoNotifyRunner", objc.GetClass("NSObject"), nil, nil,
			[]objc.MethodDef{
				// keepAlive: is the target of a repeating timer that keeps the run
				// loop from spinning when it has no other input source.
				{Cmd: sel("keepAlive:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {}},
				// onDistNote: is the observer callback for every distributed
				// subscription; it dispatches by notification name.
				{Cmd: sel("onDistNote:"), Fn: onDistNote},
			})
	})
	return runnerClass, runnerClassErr
}

// registerRunnerClass is a seam (a var) so Run's class-registration failure
// branch is testable.
var registerRunnerClass = doRegisterRunnerClass

// submit runs fn on the Run thread and blocks until it completes. It reports
// ErrNotRunning if Run is not active.
func submit(fn func()) error {
	runMu.Lock()
	ch := runTasks
	runMu.Unlock()
	if ch == nil {
		return ErrNotRunning
	}
	done := make(chan struct{})
	ch <- func() {
		fn()
		close(done)
	}
	<-done
	return nil
}

// Run drives a Foundation run loop on the calling goroutine (pinned to its OS
// thread) until ctx is cancelled, and services the Objective-C work queued by
// distributed subscriptions. It must be active for [SubscribeDistributed] to
// deliver. It returns ctx.Err() on cancellation, or [ErrAlreadyRunning] if
// another Run is in progress.
//
// Call it on a dedicated goroutine; it blocks for the lifetime of ctx.
func Run(ctx context.Context) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := loadFrameworks(); err != nil {
		return err
	}
	cls, err := registerRunnerClass()
	if err != nil {
		return err
	}

	ch := make(chan func(), 64)
	runMu.Lock()
	if runTasks != nil {
		runMu.Unlock()
		return ErrAlreadyRunning
	}
	obj := objc.ID(cls).Send(sel("alloc")).Send(sel("init"))
	obj.Send(sel("retain"))
	runObj = obj
	runTasks = ch
	runMu.Unlock()
	defer func() {
		runMu.Lock()
		runTasks = nil
		runObj = 0
		runMu.Unlock()
	}()

	// A repeating keep-alive timer gives the run loop a permanent input source,
	// so runUntilDate: blocks for the full interval instead of returning at once
	// (which would busy-spin this thread before any observer is attached).
	class("NSTimer").Send(sel("scheduledTimerWithTimeInterval:target:selector:userInfo:repeats:"),
		pumpSeconds, obj, sel("keepAlive:"), objc.ID(0), true)

	rl := class("NSRunLoop").Send(sel("currentRunLoop"))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case fn := <-ch:
			fn()
			continue
		default:
		}
		date := class("NSDate").Send(sel("dateWithTimeIntervalSinceNow:"), pumpSeconds)
		rl.Send(sel("runUntilDate:"), date)
	}
}

// ---------------------------------------------------------------------------
// NSDistributedNotificationCenter
// ---------------------------------------------------------------------------

type distEntry struct {
	token Token
	h     DistributedHandler
}

var (
	distMu       sync.Mutex
	distHandlers = map[string][]distEntry{} // notification name -> handlers
)

// onDistNote runs on the Run thread. It extracts the name and userInfo from the
// autoreleased NSNotification synchronously (the object is invalid once this
// returns) and calls every handler registered for that name.
func onDistNote(_ objc.ID, _ objc.SEL, note objc.ID) {
	name := goString(note.Send(sel("name")))
	userInfo := dictToMap(note.Send(sel("userInfo")))

	distMu.Lock()
	entries := append([]distEntry(nil), distHandlers[name]...)
	distMu.Unlock()

	for _, e := range entries {
		e.h(userInfo)
	}
}

// SubscribeDistributed registers handler for a cross-process
// NSDistributedNotificationCenter name and returns a token to [Cancel] it.
// handler is called on the [Run] thread with the notification's userInfo
// flattened to strings. [Run] must be active; otherwise it reports
// [ErrNotRunning].
func SubscribeDistributed(name string, handler DistributedHandler) (Token, error) {
	if err := validateName(name); err != nil {
		return 0, err
	}

	tok := reg.add(nil) // cleanup patched in below, once token is known
	entry := distEntry{token: tok, h: handler}

	distMu.Lock()
	first := len(distHandlers[name]) == 0
	distHandlers[name] = append(distHandlers[name], entry)
	distMu.Unlock()

	// The first handler for a name attaches the underlying Objective-C observer
	// on the Run thread; later handlers for the same name reuse it.
	if first {
		if err := submit(func() {
			center := class("NSDistributedNotificationCenter").Send(sel("defaultCenter"))
			center.Send(sel("addObserver:selector:name:object:"),
				runObj, sel("onDistNote:"), nsString(name), objc.ID(0))
		}); err != nil {
			removeDistEntry(name, tok)
			reg.take(tok)
			return 0, err
		}
	}

	// Patch the cleanup now that the subscription is live.
	patchCleanup(tok, func() { cancelDistributed(name, tok) })
	return tok, nil
}

// PostDistributed posts a cross-process NSDistributedNotificationCenter
// notification with a string userInfo dictionary, delivered immediately.
// Posting is fire-and-forget and does not require [Run].
func PostDistributed(name string, userInfo map[string]string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := validateUserInfo(userInfo); err != nil {
		return err
	}
	if err := loadFrameworks(); err != nil {
		return err
	}
	post := func() {
		center := class("NSDistributedNotificationCenter").Send(sel("defaultCenter"))
		center.Send(sel("postNotificationName:object:userInfo:deliverImmediately:"),
			nsString(name), objc.ID(0), mapToDict(userInfo), true)
	}
	// If a Run thread is active, post from it to keep all Objective-C work on one
	// thread; otherwise post directly (the call is itself thread-safe).
	if err := submit(post); err == ErrNotRunning {
		post()
	}
	return nil
}

// cancelDistributed removes one distributed handler and, when it was the last
// for its name, detaches the underlying observer on the Run thread.
func cancelDistributed(name string, tok Token) {
	last := removeDistEntry(name, tok)
	if last {
		_ = submit(func() {
			center := class("NSDistributedNotificationCenter").Send(sel("defaultCenter"))
			center.Send(sel("removeObserver:name:object:"), runObj, nsString(name), objc.ID(0))
		})
	}
}

// removeDistEntry drops the entry for tok under name and reports whether the
// name now has no handlers left.
func removeDistEntry(name string, tok Token) (last bool) {
	distMu.Lock()
	defer distMu.Unlock()
	entries := distHandlers[name]
	out := entries[:0]
	for _, e := range entries {
		if e.token != tok {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		delete(distHandlers, name)
		return true
	}
	distHandlers[name] = out
	return false
}

// patchCleanup replaces the cleanup closure stored for an existing token.
func patchCleanup(t Token, cleanup func()) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if _, ok := reg.ent[t]; ok {
		reg.ent[t] = cleanup
	}
}

// ---------------------------------------------------------------------------
// User-facing banner (best effort)
// ---------------------------------------------------------------------------

// PostUserNotification shows a user-facing banner via NSUserNotificationCenter.
//
// Honest limitation: NSUserNotificationCenter is deprecated, and macOS only
// displays the banner when the running process has a bundle identity (a proper
// .app with an Info.plist / bundle identifier). Run from a bare CLI binary,
// +defaultUserNotificationCenter typically returns nil — reported here as
// [ErrNoUserCenter] — and even when it is non-nil the system may drop the
// banner. The modern replacement, UNUserNotificationCenter, hard-requires a
// bundled, signed app and an authorization prompt, so it is intentionally not
// wrapped here. This function is a convenience for code already running inside
// an .app bundle; it never shells out to osascript.
func PostUserNotification(title, subtitle, body string) error {
	if err := loadFrameworks(); err != nil {
		return err
	}
	n := class("NSUserNotification").Send(sel("alloc")).Send(sel("init"))
	n.Send(sel("setTitle:"), nsString(title))
	if subtitle != "" {
		n.Send(sel("setSubtitle:"), nsString(subtitle))
	}
	if body != "" {
		n.Send(sel("setInformativeText:"), nsString(body))
	}
	center := userCenterLookup()
	if center == 0 {
		return ErrNoUserCenter
	}
	userCenterDeliver(center, n)
	return nil
}

// userCenterLookup and userCenterDeliver are seams over the deprecated
// NSUserNotificationCenter, so the delivery path (unreachable on a bare CLI
// binary, where the center is nil) is testable via fake-injection.
var userCenterLookup = func() objc.ID {
	return class("NSUserNotificationCenter").Send(sel("defaultUserNotificationCenter"))
}

var userCenterDeliver = func(center, note objc.ID) {
	center.Send(sel("deliverNotification:"), note)
}
