//go:build !darwin

package notify

import "context"

// This file provides the non-darwin build of the public surface so consumers
// cross-compile on any GOOS. Every entry point reports [ErrUnsupported]; the
// OS-independent helpers in notify.go (validation, the token registry, Cancel)
// remain fully functional and testable here.

// Subscribe reports ErrUnsupported on non-darwin platforms.
func Subscribe(name string, handler Handler) (Token, error) {
	if err := validateName(name); err != nil {
		return 0, err
	}
	return 0, ErrUnsupported
}

// Post reports ErrUnsupported on non-darwin platforms.
func Post(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return ErrUnsupported
}

// SubscribeDistributed reports ErrUnsupported on non-darwin platforms.
func SubscribeDistributed(name string, handler DistributedHandler) (Token, error) {
	if err := validateName(name); err != nil {
		return 0, err
	}
	return 0, ErrUnsupported
}

// PostDistributed reports ErrUnsupported on non-darwin platforms.
func PostDistributed(name string, userInfo map[string]string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := validateUserInfo(userInfo); err != nil {
		return err
	}
	return ErrUnsupported
}

// PostUserNotification reports ErrUnsupported on non-darwin platforms.
func PostUserNotification(title, subtitle, body string) error {
	return ErrUnsupported
}

// Run reports ErrUnsupported on non-darwin platforms.
func Run(ctx context.Context) error {
	return ErrUnsupported
}
