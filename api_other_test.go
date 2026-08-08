//go:build !darwin

package notify

import (
	"context"
	"errors"
	"testing"
)

// These tests run on the non-darwin lanes (linux/windows CI) and pin the stub
// behaviour: input validation still applies, and every OS entry point reports
// ErrUnsupported rather than misbehaving.

func TestStubUnsupported(t *testing.T) {
	if _, err := Subscribe("com.example.x", func() {}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Subscribe = %v, want ErrUnsupported", err)
	}
	if err := Post("com.example.x"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Post = %v, want ErrUnsupported", err)
	}
	if _, err := SubscribeDistributed("com.example.x", func(map[string]string) {}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("SubscribeDistributed = %v, want ErrUnsupported", err)
	}
	if err := PostDistributed("com.example.x", map[string]string{"k": "v"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("PostDistributed = %v, want ErrUnsupported", err)
	}
	if err := PostUserNotification("t", "s", "b"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("PostUserNotification = %v, want ErrUnsupported", err)
	}
	if err := Run(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Run = %v, want ErrUnsupported", err)
	}
}

func TestStubValidationStillApplies(t *testing.T) {
	if _, err := Subscribe("", func() {}); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("Subscribe(empty) = %v, want ErrEmptyName", err)
	}
	if err := Post("bad\x00name"); !errors.Is(err, ErrNameHasNUL) {
		t.Fatalf("Post(nul) = %v, want ErrNameHasNUL", err)
	}
	if _, err := SubscribeDistributed("", func(map[string]string) {}); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("SubscribeDistributed(empty) = %v, want ErrEmptyName", err)
	}
	if err := PostDistributed("ok.name", map[string]string{"": "v"}); !errors.Is(err, ErrBadUserInfo) {
		t.Fatalf("PostDistributed(bad userInfo) = %v, want ErrBadUserInfo", err)
	}
	// Cancel is OS-independent and idempotent even on the stub build.
	if err := Cancel(Token(999)); err != nil {
		t.Fatalf("Cancel(unknown) = %v", err)
	}
}
