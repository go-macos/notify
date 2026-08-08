//go:build darwin

package notify

// Shared purego/objc plumbing, kept intentionally identical in idiom to the
// rest of the fleet's CGO=0 macOS code (go-widgets/tray, go-news-reader window,
// go-reddit webview): dlopen the frameworks once, resolve selectors and classes
// through the Objective-C runtime, and read NSStrings via
// getCString:maxLength:encoding: (never a raw UTF8String pointer deref, which
// trips go vet's unsafeptr check).

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const nsUTF8Encoding = 4 // NSUTF8StringEncoding

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

// loadFrameworks dlopens Foundation and libSystem and binds the notify(3)
// symbols. Safe to call repeatedly; the work happens once.
func loadFrameworks() error {
	frameworksOnce.Do(func() {
		if _, err := purego.Dlopen(
			"/System/Library/Frameworks/Foundation.framework/Foundation",
			purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
			frameworksErr = fmt.Errorf("notify: dlopen Foundation: %w", err)
			return
		}
		sys, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			frameworksErr = fmt.Errorf("notify: dlopen libSystem: %w", err)
			return
		}
		purego.RegisterLibFunc(&cNotifyRegisterCheck, sys, "notify_register_check")
		purego.RegisterLibFunc(&cNotifyCheck, sys, "notify_check")
		purego.RegisterLibFunc(&cNotifyPost, sys, "notify_post")
		purego.RegisterLibFunc(&cNotifyCancel, sys, "notify_cancel")
	})
	return frameworksErr
}

func sel(name string) objc.SEL { return objc.RegisterName(name) }

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

// nsString builds an autoreleased NSString from a Go string.
func nsString(s string) objc.ID {
	return class("NSString").Send(sel("stringWithUTF8String:"), s)
}

// goString copies an NSString's UTF-8 bytes into a Go-owned buffer. Returns ""
// for a nil id or an empty string.
func goString(id objc.ID) string {
	if id == 0 {
		return ""
	}
	n := int(id.Send(sel("lengthOfBytesUsingEncoding:"), nsUTF8Encoding))
	if n <= 0 {
		return ""
	}
	buf := make([]byte, n+1)
	if id.Send(sel("getCString:maxLength:encoding:"), unsafe.Pointer(&buf[0]), len(buf), nsUTF8Encoding) == 0 {
		return ""
	}
	return string(buf[:n])
}

// objcStringify renders any id as a Go string: NSStrings directly, anything
// else through -description (so a non-string userInfo value degrades to its
// textual form rather than crashing the getCString path).
func objcStringify(v objc.ID) string {
	if v == 0 {
		return ""
	}
	if v.Send(sel("isKindOfClass:"), class("NSString")) != 0 {
		return goString(v)
	}
	return goString(v.Send(sel("description")))
}

// dictToMap flattens an NSDictionary to map[string]string using objcStringify
// for both keys and values.
func dictToMap(dict objc.ID) map[string]string {
	if dict == 0 {
		return map[string]string{}
	}
	keys := dict.Send(sel("allKeys"))
	n := int(keys.Send(sel("count")))
	m := make(map[string]string, n)
	for i := 0; i < n; i++ {
		k := keys.Send(sel("objectAtIndex:"), i)
		v := dict.Send(sel("objectForKey:"), k)
		m[objcStringify(k)] = objcStringify(v)
	}
	return m
}

// mapToDict builds an autoreleased NSMutableDictionary of NSString→NSString.
func mapToDict(m map[string]string) objc.ID {
	dict := class("NSMutableDictionary").Send(sel("dictionary"))
	for k, v := range m {
		dict.Send(sel("setObject:forKey:"), nsString(v), nsString(k))
	}
	return dict
}
