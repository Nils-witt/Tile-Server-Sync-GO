//go:build windows

// Windows service integration. Tile-Server-Sync-GO can install itself as a
// Windows service (`-service install`) so it runs unattended in the
// background under the interval loop (see main.go's run/runLoop), instead
// of requiring a foreground console session.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "Tile-Server-Sync-GO"

// stopTimeout bounds how long the service waits for an in-flight run() to
// react to context cancellation (and, separately, for the SCM stop call to
// observe the service reaching the Stopped state) before giving up.
const stopTimeout = 30 * time.Second

func isWindowsService() (bool, error) {
	return svc.IsWindowsService()
}

// winService adapts run() to the svc.Handler interface expected by the
// Windows Service Control Manager.
type winService struct {
	configPath string
	elog       *eventlog.Log
}

func (s *winService) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, s.configPath) }()

	changes <- svc.Status{State: svc.Running, Accepts: accepted}

	s.logInfo("Tile-Server-Sync-GO service started")

	for {
		select {
		case err := <-errCh:
			if err != nil {
				s.logError(fmt.Sprintf("run exited with error: %v", err))
			}

			changes <- svc.Status{State: svc.Stopped}

			return false, 0
		case req := <-r:
			if stop := s.handleRequest(req, changes, cancel, errCh); stop {
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		}
	}
}

// handleRequest processes one SCM change request. It returns true once the
// service should shut down (i.e. after a Stop/Shutdown request has been
// acted on).
func (s *winService) handleRequest(
	req svc.ChangeRequest,
	changes chan<- svc.Status,
	cancel context.CancelFunc,
	errCh <-chan error,
) bool {
	switch req.Cmd { //nolint:exhaustive // only Interrogate/Stop/Shutdown are meaningful here; everything else is ignored
	case svc.Interrogate:
		changes <- req.CurrentStatus
		return false
	case svc.Stop, svc.Shutdown:
		changes <- svc.Status{State: svc.StopPending}

		cancel()

		select {
		case <-errCh:
		case <-time.After(stopTimeout):
		}

		s.logInfo("Tile-Server-Sync-GO service stopped")

		return true
	default:
		return false
	}
}

func (s *winService) logInfo(msg string) {
	if s.elog != nil {
		_ = s.elog.Info(1, msg)
	}
}

func (s *winService) logError(msg string) {
	if s.elog != nil {
		_ = s.elog.Error(1, msg)
	}
}

// runAsService blocks, running Tile-Server-Sync-GO under SCM control, until the
// service is stopped.
func runAsService(configPath string) error {
	elog, err := eventlog.Open(serviceName)
	if err != nil {
		// Not fatal: the service still runs, just without event log
		// messages (e.g. the event source was never installed).
		elog = nil
	} else {
		defer func() { _ = elog.Close() }()
	}

	return svc.Run(serviceName, &winService{configPath: configPath, elog: elog})
}

func installService(configPath string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	if existing, err := m.OpenService(serviceName); err == nil {
		_ = existing.Close()
		return fmt.Errorf("service %s is already installed", serviceName)
	}

	s, err := m.CreateService(serviceName, exePath, mgr.Config{
		DisplayName: "Tile-Server-Sync-GO",
		Description: "Syncs geo objects from tileserve-go into MariaDB.",
		StartType:   mgr.StartAutomatic,
	}, "-service", "run", "-config", absConfigPath)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer func() { _ = s.Close() }()

	if err := eventlog.InstallAsEventCreate(serviceName, eventlog.Info|eventlog.Warning|eventlog.Error); err != nil {
		log.Printf("warning: failed to install event log source: %v", err)
	}

	return nil
}

func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %s is not installed: %w", serviceName, err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}

	_ = eventlog.Remove(serviceName)

	return nil
}

func startService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service %s: %w", serviceName, err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}

	return nil
}

func stopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service %s: %w", serviceName, err)
	}
	defer func() { _ = s.Close() }()

	status, err := s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("send stop control: %w", err)
	}

	deadline := time.Now().Add(stopTimeout)
	for status.State != svc.Stopped {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for service %s to stop", serviceName)
		}

		time.Sleep(300 * time.Millisecond)

		status, err = s.Query()
		if err != nil {
			return fmt.Errorf("query service status: %w", err)
		}
	}

	return nil
}
