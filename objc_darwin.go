//go:build darwin

package notify

// Darwin-specific plumbing. The generic Objective-C runtime bridging —
// selector/class lookup, NSString<->Go conversion and the NSDictionary
// helpers — now lives in the shared github.com/go-macos/objc library (this
// package was its reference implementation); the thin wrappers below adapt it
// to the local spelling so the rest of the package reads unchanged. What
// remains genuinely notify-specific stays here: the notify(3) C functions and
// the one-time framework/dylib load that binds them.

import (
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
	objc "github.com/go-macos/objc"
)

// notify(3) status: NOTIFY_STATUS_OK.
const notifyStatusOK = 0

// notify(3) C functions, bound once via purego against libSystem.
//
// Subscriptions use notify_register_check + notify_check (a shared-memory
// "fired since last check?" flag) polled on a ticker, deliberately NOT
// notify_register_file_descriptor / _dispatch. The fd/mach-port variants are
// mach-port-backed, and having such a descriptor live in the process — whether
// registered with the Go netpoller (os.File) or read with a raw blocking
// syscall that is in flight at teardown — reliably breaks CFRunLoop mach
// delivery for NSDistributedNotificationCenter in the same process (verified
// exhaustively). The shared-memory check path touches no fd and no mach port,
// so the two mechanisms coexist. The cost is up to one poll interval of latency
// (see PollInterval) and per-interval coalescing of repeated posts — which
// matches notify(3)'s own coalescing semantics.
var (
	cNotifyRegisterCheck func(name string, token *int32) int32
	cNotifyCheck         func(token int32, check *int32) int32
	cNotifyPost          func(name string) int32
	cNotifyCancel        func(token int32) int32
)

var (
	frameworksOnce sync.Once
	frameworksErr  error
)

// dlopenFn is a seam over purego.Dlopen so the dlopen-failure branches in
// doLoadFrameworks are reachable from tests (fake-injection).
var dlopenFn = purego.Dlopen

const libSystemPath = "/usr/lib/libSystem.B.dylib"

// doLoadFrameworks dlopens Foundation (for the Objective-C classes the shared
// objc helpers reach) and libSystem, and binds the notify(3) symbols. It runs
// exactly once, driven by loadFrameworks.
func doLoadFrameworks() error {
	if _, err := dlopenFn(objc.Foundation, purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		return fmt.Errorf("notify: dlopen Foundation: %w", err)
	}
	sys, err := dlopenFn(libSystemPath, purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return fmt.Errorf("notify: dlopen libSystem: %w", err)
	}
	purego.RegisterLibFunc(&cNotifyRegisterCheck, sys, "notify_register_check")
	purego.RegisterLibFunc(&cNotifyCheck, sys, "notify_check")
	purego.RegisterLibFunc(&cNotifyPost, sys, "notify_post")
	purego.RegisterLibFunc(&cNotifyCancel, sys, "notify_cancel")
	return nil
}

// loadFrameworks is a package-level var (a seam) so every caller's
// "if err := loadFrameworks(); err != nil" branch is testable by swapping it to
// return an error. Safe to call repeatedly; the real work happens once.
var loadFrameworks = func() error {
	frameworksOnce.Do(func() { frameworksErr = doLoadFrameworks() })
	return frameworksErr
}

// The bridging helpers below delegate to github.com/go-macos/objc. They keep
// the package's original names and signatures so api_darwin.go is unchanged;
// because go-macos/objc's ID/SEL types are aliases of purego/objc's, the types
// line up exactly.

func sel(name string) objc.SEL { return objc.Sel(name) }

func class(name string) objc.ID { return objc.ClassID(name) }

// nsString builds an autoreleased NSString from a Go string.
func nsString(s string) objc.ID { return objc.NSString(s) }

// goString copies an NSString's UTF-8 bytes into a Go-owned buffer. Returns ""
// for a nil id or an empty string.
func goString(id objc.ID) string { return objc.GoString(id) }

// objcStringify renders any id as a Go string: NSStrings directly, anything
// else through -description.
func objcStringify(v objc.ID) string { return objc.Stringify(v) }

// dictToMap flattens an NSDictionary to map[string]string.
func dictToMap(dict objc.ID) map[string]string { return objc.DictToMap(dict) }

// mapToDict builds an autoreleased NSMutableDictionary of NSString->NSString.
func mapToDict(m map[string]string) objc.ID { return objc.MapToDict(m) }
