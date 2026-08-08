package notify

import (
	"errors"
	"strings"
	"sync"
)

// Token identifies a live subscription. It is opaque; pass it to [Cancel].
type Token int

// Handler is invoked for each delivery of a notify(3) subscription. The
// Darwin notify bus carries no payload, so a handler receives no arguments.
// It runs on a background goroutine dedicated to the subscription.
type Handler func()

// DistributedHandler is invoked for each delivery of an
// NSDistributedNotificationCenter subscription, receiving the notification's
// userInfo dictionary flattened to strings. It runs on the [Run] thread.
type DistributedHandler func(userInfo map[string]string)

// Errors returned by the package. They are stable and may be tested with
// errors.Is.
var (
	ErrEmptyName      = errors.New("notify: empty notification name")
	ErrNameTooLong    = errors.New("notify: notification name too long")
	ErrNameHasNUL     = errors.New("notify: notification name contains NUL byte")
	ErrBadUserInfo    = errors.New("notify: userInfo has an empty key or a NUL byte")
	ErrUnsupported    = errors.New("notify: unsupported on this platform (darwin only)")
	ErrNotRunning     = errors.New("notify: Run is not active (required for distributed subscriptions)")
	ErrAlreadyRunning = errors.New("notify: Run is already active")
	ErrRegister       = errors.New("notify: registration failed")
	ErrPost           = errors.New("notify: post failed")
	ErrNoUserCenter   = errors.New("notify: no NSUserNotificationCenter (process needs a bundle identity)")
)

// maxNameLen is a defensive cap on notification names. Darwin keys are short
// reverse-DNS strings (e.g. com.apple.system.timezone); this bound only rejects
// obviously malformed input before it reaches the C layer.
const maxNameLen = 512

// validateName rejects names the C bridge cannot carry: empty, over-long, or
// containing a NUL (stringWithUTF8String / the C name argument both terminate
// at the first NUL, so an embedded NUL would silently truncate the name).
func validateName(name string) error {
	switch {
	case name == "":
		return ErrEmptyName
	case len(name) > maxNameLen:
		return ErrNameTooLong
	case strings.IndexByte(name, 0) >= 0:
		return ErrNameHasNUL
	}
	return nil
}

// validateUserInfo ensures every key/value can survive the UTF-8 C-string
// bridge: keys must be non-empty and neither keys nor values may contain a NUL.
func validateUserInfo(u map[string]string) error {
	for k, v := range u {
		if k == "" || strings.IndexByte(k, 0) >= 0 || strings.IndexByte(v, 0) >= 0 {
			return ErrBadUserInfo
		}
	}
	return nil
}

// registry is the OS-independent token bookkeeping shared by every subscription
// kind. Each token maps to a cleanup closure that tears the subscription down.
type registry struct {
	mu   sync.Mutex
	next Token
	ent  map[Token]func()
}

func newRegistry() *registry { return &registry{ent: make(map[Token]func())} }

// add stores cleanup under a fresh token and returns it. Tokens are strictly
// increasing and never reused within a process.
func (r *registry) add(cleanup func()) Token {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	t := r.next
	r.ent[t] = cleanup
	return t
}

// take removes and returns the cleanup for t. ok is false if t is unknown
// (already cancelled or never issued), which makes cancellation idempotent.
func (r *registry) take(t Token) (cleanup func(), ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cleanup, ok = r.ent[t]
	if ok {
		delete(r.ent, t)
	}
	return cleanup, ok
}

// len reports the number of live subscriptions.
func (r *registry) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ent)
}

// reg holds every live subscription for the process.
var reg = newRegistry()

// Cancel tears down the subscription identified by t and releases its token.
// It is idempotent: cancelling an unknown or already-cancelled token is a
// no-op that returns nil. Safe to call from any goroutine and on any platform.
func Cancel(t Token) error {
	cleanup, ok := reg.take(t)
	if !ok {
		return nil
	}
	cleanup()
	return nil
}
