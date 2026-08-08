// Package notify is a pure-Go (CGO_ENABLED=0) interop layer for the macOS
// notification and event-bus mechanisms. It reaches the OS entirely through
// github.com/ebitengine/purego — dlopen + objc_msgSend and dlsym'd libSystem C
// functions — so it links with no cgo and no shelling out to osascript.
//
// It is the macOS counterpart to the Linux go-freedesktop/notifications work
// and exposes three distinct mechanisms:
//
//   - The Darwin notify(3) event bus (Subscribe/Post/Cancel). This is the
//     low-level, payload-free named-signal bus that carries keys such as
//     com.apple.system.timezone. Subscriptions are delivered over a kernel file
//     descriptor read by a background goroutine, so they need no run loop.
//
//   - NSDistributedNotificationCenter (SubscribeDistributed/PostDistributed).
//     Cross-process named notifications that carry a string userInfo dictionary.
//     Delivery is driven by a Foundation run loop, so a subscriber must have an
//     active [Run] on a dedicated thread; posting is fire-and-forget and needs
//     no run loop.
//
//   - A best-effort user-facing banner via NSUserNotificationCenter
//     ([PostUserNotification]). See that function for the honest bundle-identity
//     limitation.
//
// Every exported symbol is defined on all platforms so consumers cross-compile;
// on non-darwin GOOS the functions return [ErrUnsupported].
//
// # notify(3): why a file descriptor, not a dispatch block
//
// The classic notify_register_dispatch takes an Objective-C dispatch block. A
// block is not a plain C function pointer — it is an object carrying an invoke
// pointer and a descriptor — and it cannot be synthesised from Go under
// CGO_ENABLED=0 without hand-assembling the block ABI. notify(3) also exposes
// notify_register_file_descriptor, which delivers each event by writing the
// 4-byte registration token (network byte order) to a file descriptor. That is
// the idiomatic pure-Go source: a goroutine blocks on the fd and calls the
// handler on each event, with no dispatch queue and no run loop. This package
// uses it.
package notify
