//go:build !race

package notify

// raceEnabled reports whether the test binary was built with -race. The macOS
// mach-port delivery paths (NSDistributedNotificationCenter, and notify(3)'s
// interaction with it) time out unreliably under the race runtime's altered
// scheduling — not because of any Go data race (the suite reports none), but
// because CFRunLoop/distnoted delivery is sensitive to it. Those OS round-trip
// tests skip under -race and are covered fully in the CGO=0 lane; the
// OS-independent logic (validation, registry, dict marshalling) still runs
// under -race, which is where race detection has value.
const raceEnabled = false
