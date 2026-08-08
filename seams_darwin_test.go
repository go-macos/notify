//go:build darwin

package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// These tests drive the OS-failure branches through the package's fake-injection
// seams (loadFrameworks, dlopenFn, the notify(3) C-func vars, registerRunnerClass,
// objcGetCString, and the user-notification center seams). Each swaps a seam,
// exercises the branch, and restores it — the real on-device round-trips in
// api_darwin_test.go are untouched.

// withLoadFrameworksErr swaps loadFrameworks to fail for the duration of fn.
func withLoadFrameworksErr(fn func()) {
	orig := loadFrameworks
	loadFrameworks = func() error { return errors.New("boom: frameworks") }
	defer func() { loadFrameworks = orig }()
	fn()
}

func TestSeam_LoadFrameworksErrorPropagates(t *testing.T) {
	boom := "boom: frameworks"
	withLoadFrameworksErr(func() {
		if _, err := Subscribe("com.example.x", func() {}); err == nil || err.Error() != boom {
			t.Fatalf("Subscribe loadFrameworks err = %v", err)
		}
		if err := Post("com.example.x"); err == nil || err.Error() != boom {
			t.Fatalf("Post loadFrameworks err = %v", err)
		}
		if err := PostDistributed("com.example.x", nil); err == nil || err.Error() != boom {
			t.Fatalf("PostDistributed loadFrameworks err = %v", err)
		}
		if err := PostUserNotification("t", "", ""); err == nil || err.Error() != boom {
			t.Fatalf("PostUserNotification loadFrameworks err = %v", err)
		}
		if err := Run(context.Background()); err == nil || err.Error() != boom {
			t.Fatalf("Run loadFrameworks err = %v", err)
		}
	})
}

func TestSeam_DoLoadFrameworksDlopenErrors(t *testing.T) {
	orig := dlopenFn
	defer func() { dlopenFn = orig }()

	// Foundation dlopen fails.
	dlopenFn = func(string, int) (uintptr, error) { return 0, errors.New("no fw") }
	if err := doLoadFrameworks(); err == nil || !errRefsPath(err, "Foundation") {
		t.Fatalf("doLoadFrameworks Foundation err = %v", err)
	}

	// Foundation ok, libSystem fails.
	var calls int
	dlopenFn = func(string, int) (uintptr, error) {
		calls++
		if calls == 1 {
			return 1, nil // pretend Foundation loaded
		}
		return 0, errors.New("no libSystem")
	}
	if err := doLoadFrameworks(); err == nil || !errRefsPath(err, "libSystem") {
		t.Fatalf("doLoadFrameworks libSystem err = %v", err)
	}
}

func errRefsPath(err error, want string) bool {
	return err != nil && contains(err.Error(), want)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestSeam_SubscribeRegisterFails(t *testing.T) {
	if err := loadFrameworks(); err != nil {
		t.Fatalf("loadFrameworks: %v", err)
	}
	orig := cNotifyRegisterCheck
	defer func() { cNotifyRegisterCheck = orig }()
	cNotifyRegisterCheck = func(string, *int32) int32 { return 7 } // non-OK status
	if _, err := Subscribe("com.example.regfail", func() {}); !errors.Is(err, ErrRegister) {
		t.Fatalf("Subscribe register-fail = %v, want ErrRegister", err)
	}
}

func TestSeam_PostFails(t *testing.T) {
	if err := loadFrameworks(); err != nil {
		t.Fatalf("loadFrameworks: %v", err)
	}
	orig := cNotifyPost
	defer func() { cNotifyPost = orig }()
	cNotifyPost = func(string) int32 { return 3 } // non-OK status
	if err := Post("com.example.postfail"); !errors.Is(err, ErrPost) {
		t.Fatalf("Post fail = %v, want ErrPost", err)
	}
}

func TestSeam_RunRegisterClassFails(t *testing.T) {
	orig := registerRunnerClass
	defer func() { registerRunnerClass = orig }()
	registerRunnerClass = func() (objc.Class, error) { return 0, errors.New("no class") }
	if err := Run(context.Background()); err == nil || err.Error() != "no class" {
		t.Fatalf("Run registerRunnerClass err = %v", err)
	}
}

func TestSeam_GoStringGetCStringFails(t *testing.T) {
	if err := loadFrameworks(); err != nil {
		t.Fatalf("loadFrameworks: %v", err)
	}
	orig := objcGetCString
	defer func() { objcGetCString = orig }()
	objcGetCString = func(objc.ID, []byte) bool { return false }
	if got := goString(nsString("nonempty")); got != "" {
		t.Fatalf("goString with failing getCString = %q, want empty", got)
	}
}

func TestSeam_PostUserNotificationDeliverPath(t *testing.T) {
	if err := loadFrameworks(); err != nil {
		t.Fatalf("loadFrameworks: %v", err)
	}
	origLookup, origDeliver := userCenterLookup, userCenterDeliver
	defer func() { userCenterLookup, userCenterDeliver = origLookup, origDeliver }()

	var delivered bool
	userCenterLookup = func() objc.ID { return objc.ID(1) } // non-nil fake center
	userCenterDeliver = func(center, note objc.ID) {
		if center != objc.ID(1) || note == 0 {
			t.Errorf("deliver got center=%v note=%v", center, note)
		}
		delivered = true
	}
	if err := PostUserNotification("title", "subtitle", "body"); err != nil {
		t.Fatalf("PostUserNotification deliver path = %v", err)
	}
	if !delivered {
		t.Fatal("userCenterDeliver was not called")
	}
}

// TestSeam_UserCenterDeliverRealBody executes the real userCenterDeliver
// closure (the actual -deliverNotification: send). A bundle-less test binary
// never has a live NSUserNotificationCenter, so this drives it with a nil
// receiver, which the Objective-C runtime treats as a safe no-op — enough to
// cover the statement without depending on an .app bundle.
func TestSeam_UserCenterDeliverRealBody(t *testing.T) {
	if err := loadFrameworks(); err != nil {
		t.Fatalf("loadFrameworks: %v", err)
	}
	note := class("NSUserNotification").Send(sel("alloc")).Send(sel("init"))
	userCenterDeliver(objc.ID(0), note) // nil center: no-op, exercises the real send
}

// TestNotifyZeroPollInterval covers the "PollInterval <= 0 -> default" branch
// in Subscribe, and proves delivery still works with the fallback interval.
// It does a real round trip, so it self-skips under -race (like the other
// mach/poll delivery tests) and is declared here (this file sorts after
// api_darwin_test.go) so it runs after all distributed-delivery tests.
func TestNotifyZeroPollInterval(t *testing.T) {
	if raceEnabled {
		t.Skip("notify(3) check-poll delivery is unreliable under -race; covered in the CGO=0 lane")
	}
	orig := PollInterval
	defer func() { PollInterval = orig }()
	PollInterval = 0 // exercise the <=0 fallback to the default interval

	name := uniqueName("zeropoll")
	fired := make(chan struct{}, 4)
	tok, err := Subscribe(name, func() { fired <- struct{}{} })
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer Cancel(tok)
	if err := Post(name); err != nil {
		t.Fatalf("Post: %v", err)
	}
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not fire with zero PollInterval (default fallback)")
	}
}

// ensure purego stays referenced (dlopenFn seam uses its signature type).
var _ = purego.RTLD_NOW
