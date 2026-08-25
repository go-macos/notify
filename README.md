# notify

[![CI](https://github.com/go-macos/notify/actions/workflows/ci.yml/badge.svg)](https://github.com/go-macos/notify/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-macos/notify.svg)](https://pkg.go.dev/github.com/go-macos/notify)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)

Pure-Go (`CGO_ENABLED=0`) interop with the macOS notification and event-bus
mechanisms. Everything is reached through
[`ebitengine/purego`](https://github.com/ebitengine/purego) — `dlopen` +
`objc_msgSend` and `dlsym`'d libSystem C functions — so it links with **no cgo**
and never shells out to `osascript`. It is the macOS counterpart to the Linux
`go-freedesktop/notifications` work.

## Mechanisms

| API | Backing | Cross-process | Payload | Needs `Run`? |
| --- | --- | --- | --- | --- |
| `Subscribe` / `Post` / `Cancel` | Darwin `notify(3)` | yes | none (named signal) | no |
| `SubscribeDistributed` / `PostDistributed` | `NSDistributedNotificationCenter` | yes | `map[string]string` userInfo | subscribe: yes; post: no |
| `PostUserNotification` | `NSUserNotificationCenter` | — | title/subtitle/body | no (best-effort, see below) |

```go
// notify(3) event bus — subscribe to a system key, no run loop needed.
tok, _ := notify.Subscribe("com.apple.system.timezone", func() {
    log.Println("time zone changed")
})
defer notify.Cancel(tok)

// NSDistributedNotificationCenter — cross-process, with a userInfo payload.
go notify.Run(ctx) // drives the Foundation run loop on its own thread
notify.SubscribeDistributed("com.example.event", func(ui map[string]string) {
    log.Println("got", ui)
})
notify.PostDistributed("com.example.event", map[string]string{"k": "v"})
```

## Design notes

- **notify(3) uses `notify_register_check` polled every `PollInterval`
  (default 50 ms), not `notify_register_dispatch` or
  `notify_register_file_descriptor`.** A dispatch block cannot be synthesised
  under `CGO_ENABLED=0`, and — verified exhaustively on-device — a
  mach-port-backed notify file descriptor living in the process breaks
  `NSDistributedNotificationCenter` mach delivery. The shared-memory check path
  touches no fd and no mach port. The trade-off is up to one poll interval of
  latency and per-interval coalescing of repeated posts, which matches
  `notify(3)`'s own coalescing.

- **`Run(ctx)`** drives a Foundation run loop pinned to its goroutine's OS
  thread and services the Objective-C work queued by distributed subscriptions.
  Call it once on a dedicated goroutine; it blocks until `ctx` is cancelled.
  Distributed *posting* does not require it.

- **`PostUserNotification` is best-effort and deprecated-API-based.** macOS only
  shows the banner when the process has a bundle identity (a real `.app` with an
  `Info.plist`); from a bare CLI `+defaultUserNotificationCenter` usually returns
  nil, reported as `ErrNoUserCenter`. The modern `UNUserNotificationCenter`
  hard-requires a bundled, signed app plus an authorization prompt, so it is
  intentionally not wrapped.

### Known limitation: mixing `notify(3)` and distributed in one process

Initialising libnotify's registration machinery can disrupt
`NSDistributedNotificationCenter` delivery that is established *afterwards* in
the same process — a macOS libnotify/distnoted interaction, not a bug in this
package (each mechanism is fully reliable on its own). If a process needs both,
start `Run` and your distributed subscriptions **before** the first `Subscribe`,
or isolate the two in separate processes.

## Platforms

Darwin only. Every exported symbol is defined on all platforms so consumers
cross-compile; on non-darwin `GOOS` the functions return `ErrUnsupported`.

## Testing

The darwin lane runs real, on-device round trips (post → receive) for both the
`notify(3)` bus and `NSDistributedNotificationCenter`, asserting the handler
fires and the userInfo survives. The OS-independent logic (name validation,
token bookkeeping, dictionary marshalling) is covered to 100%. `CGO_ENABLED=0`
throughout — purego needs no cgo.

```
CGO_ENABLED=0 go test ./...
```

## License

BSD-3-Clause. See [LICENSE](LICENSE).
