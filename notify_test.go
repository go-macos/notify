package notify

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"ok", "com.apple.system.timezone", nil},
		{"empty", "", ErrEmptyName},
		{"nul", "com.example\x00x", ErrNameHasNUL},
		{"toolong", strings.Repeat("a", maxNameLen+1), ErrNameTooLong},
		{"exactlyMax", strings.Repeat("a", maxNameLen), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := validateName(c.in); !errors.Is(got, c.want) {
				t.Fatalf("validateName(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestValidateUserInfo(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]string
		want error
	}{
		{"nil", nil, nil},
		{"ok", map[string]string{"k": "v", "empty-value-ok": ""}, nil},
		{"emptyKey", map[string]string{"": "v"}, ErrBadUserInfo},
		{"nulKey", map[string]string{"a\x00b": "v"}, ErrBadUserInfo},
		{"nulVal", map[string]string{"k": "a\x00b"}, ErrBadUserInfo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := validateUserInfo(c.in); !errors.Is(got, c.want) {
				t.Fatalf("validateUserInfo = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	r := newRegistry()
	if r.len() != 0 {
		t.Fatalf("new registry len = %d, want 0", r.len())
	}

	var ran1, ran2 int
	t1 := r.add(func() { ran1++ })
	t2 := r.add(func() { ran2++ })
	if t1 == t2 {
		t.Fatalf("tokens not unique: %d == %d", t1, t2)
	}
	if r.len() != 2 {
		t.Fatalf("len = %d, want 2", r.len())
	}

	c, ok := r.take(t1)
	if !ok {
		t.Fatal("take(t1) not found")
	}
	c()
	if ran1 != 1 {
		t.Fatalf("cleanup1 ran %d times, want 1", ran1)
	}
	if r.len() != 1 {
		t.Fatalf("len after take = %d, want 1", r.len())
	}

	// Taking an already-taken token yields ok=false (idempotency substrate).
	if _, ok := r.take(t1); ok {
		t.Fatal("take(t1) second time returned ok=true")
	}
	// Unknown token.
	if _, ok := r.take(Token(9999)); ok {
		t.Fatal("take(unknown) returned ok=true")
	}
	_ = t2
}

func TestCancelIdempotent(t *testing.T) {
	// Cancel of an unknown token is a no-op returning nil.
	if err := Cancel(Token(123456)); err != nil {
		t.Fatalf("Cancel(unknown) = %v, want nil", err)
	}

	var ran int
	tok := reg.add(func() { ran++ })
	if err := Cancel(tok); err != nil {
		t.Fatalf("Cancel = %v", err)
	}
	if ran != 1 {
		t.Fatalf("cleanup ran %d times, want 1", ran)
	}
	// Second cancel is a no-op.
	if err := Cancel(tok); err != nil {
		t.Fatalf("second Cancel = %v", err)
	}
	if ran != 1 {
		t.Fatalf("cleanup ran %d times after double cancel, want 1", ran)
	}
}
