//go:build !windows

// Non-Windows stand-ins for the Windows service management functions in
// service_windows.go. Windows service support only makes sense when built
// for windows (see .goreleaser.yaml), so every other platform keeps
// running go-sync-objects as a plain foreground process.
package main

import "errors"

var errServiceUnsupported = errors.New("windows service support is only available when built for windows (GOOS=windows)")

func isWindowsService() (bool, error) {
	return false, nil
}

func installService(_ string) error {
	return errServiceUnsupported
}

func uninstallService() error {
	return errServiceUnsupported
}

func startService() error {
	return errServiceUnsupported
}

func stopService() error {
	return errServiceUnsupported
}

func runAsService(_ string) error {
	return errServiceUnsupported
}
