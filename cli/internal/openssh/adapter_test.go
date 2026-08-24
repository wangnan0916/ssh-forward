//go:build darwin || linux

package openssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestForwardsShareMasterAndCancelIndependently(t *testing.T) {
	controlDirectory := t.TempDir()
	executable := fakeSSHExecutable(t, controlDirectory)
	adapter, err := New(Options{
		Executable: executable, ControlDirectory: controlDirectory,
		ReadyTimeout: 10 * time.Second, WaitDelay: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close(context.Background()) })

	firstPort, releaseFirstPort := reservedPort(t)
	defer releaseFirstPort()
	secondPort, releaseSecondPort := reservedPort(t)
	defer releaseSecondPort()
	firstTarget := core.ForwardTarget{RemotePort: 38080, LocalPort: firstPort}
	secondTarget := core.ForwardTarget{RemotePort: 38082, LocalPort: secondPort}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	firstReady := make(chan struct{})
	secondReady := make(chan struct{})
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		firstDone <- adapter.Forward(firstCtx, "dev", firstTarget, func(uint16) { close(firstReady) })
	}()
	go func() {
		secondDone <- adapter.Forward(secondCtx, "dev", secondTarget, func(uint16) { close(secondReady) })
	}()
	waitClosed(t, firstReady)
	waitClosed(t, secondReady)

	cancelFirst()
	if err := waitError(t, firstDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("first forward error = %v", err)
	}
	select {
	case err := <-secondDone:
		t.Fatalf("second forward stopped with first: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancelSecond()
	if err := waitError(t, secondDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("second forward error = %v", err)
	}
	if err := adapter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	events := readEvents(t, controlDirectory)
	if countEvent(events, "master") != 1 {
		t.Fatalf("events = %v, want one master", events)
	}
	for _, target := range []core.ForwardTarget{firstTarget, secondTarget} {
		specification := fmt.Sprintf(
			"127.0.0.1:%d:127.0.0.1:%d", target.LocalPort, target.RemotePort,
		)
		if countEvent(events, "forward="+specification) != 1 ||
			countEvent(events, "cancel="+specification) != 1 {
			t.Fatalf("events = %v, want forward and cancel for %s", events, specification)
		}
	}
}

func TestForwardFallsBackToNextLocalPortWhenAllowed(t *testing.T) {
	controlDirectory := t.TempDir()
	adapter := newTestAdapter(t, controlDirectory)
	actualPort, releasePort := reservedPort(t)
	defer releasePort()
	preferredPort := actualPort - 1
	failNextForward(t, controlDirectory)
	target := core.ForwardTarget{
		RemotePort: 38080, LocalPort: preferredPort, AllowFallback: true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan uint16, 1)
	done := make(chan error, 1)
	go func() {
		done <- adapter.Forward(ctx, "dev", target, func(localPort uint16) { ready <- localPort })
	}()
	select {
	case got := <-ready:
		if got != actualPort {
			t.Fatalf("actual local port = %d, want %d", got, actualPort)
		}
	case err := <-done:
		t.Fatalf("forward failed before fallback: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("forward did not become ready")
	}
	cancel()
	if err := waitError(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("forward error = %v", err)
	}

	events := readEvents(t, controlDirectory)
	wantForwardLifecycle(t, events, preferredPort, target.RemotePort)
	wantForwardLifecycle(t, events, actualPort, target.RemotePort)
}

func TestForwardDoesNotFallBackWhenLocalPortIsExplicit(t *testing.T) {
	controlDirectory := t.TempDir()
	adapter := newTestAdapter(t, controlDirectory)
	fallbackPort, releasePort := reservedPort(t)
	defer releasePort()
	preferredPort := fallbackPort - 1
	failNextForward(t, controlDirectory)
	target := core.ForwardTarget{RemotePort: 38080, LocalPort: preferredPort}

	err := adapter.Forward(context.Background(), "dev", target, func(uint16) {
		t.Fatal("strict forward unexpectedly became ready")
	})
	if got := core.ErrorDiagnostic(err); got != "local_port_conflict" {
		t.Fatalf("diagnostic = %q, want local_port_conflict", got)
	}
	events := readEvents(t, controlDirectory)
	wantForwardLifecycle(t, events, preferredPort, target.RemotePort)
	fallback := fmt.Sprintf("forward=127.0.0.1:%d:127.0.0.1:%d", fallbackPort, target.RemotePort)
	if countEvent(events, fallback) != 0 {
		t.Fatalf("events = %v, strict target tried fallback port", events)
	}
}

func TestNewRejectsSharedWritableControlDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o770); err != nil {
		t.Fatal(err)
	}
	_, err := New(Options{Executable: "/usr/bin/ssh", ControlDirectory: directory})
	if err == nil || !strings.Contains(err.Error(), "must not be writable by other users") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenSSHHelperProcess(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		return
	}
	arguments := os.Args[separator+1:]
	operation := controlOperation(arguments)
	event := operation
	if forward := controlForward(arguments); forward != "" {
		event += "=" + forward
	}
	appendEvent(event)
	if operation == "forward" && os.Remove("fail-next-forward") == nil {
		_, _ = fmt.Fprintln(os.Stderr, "muxclient: master forward request failed")
		os.Exit(1)
	}
	switch operation {
	case "master":
		if err := os.WriteFile("master.ready", nil, 0o600); err != nil {
			os.Exit(1)
		}
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
		<-signals
		os.Exit(0)
	case "check":
		if _, err := os.Stat("master.ready"); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	case "exit":
		os.Exit(1)
	default:
		os.Exit(0)
	}
}

func newTestAdapter(t *testing.T, controlDirectory string) *Adapter {
	t.Helper()
	adapter, err := New(Options{
		Executable: fakeSSHExecutable(t, controlDirectory), ControlDirectory: controlDirectory,
		ReadyTimeout: 10 * time.Second, WaitDelay: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close(context.Background()) })
	return adapter
}

func failNextForward(t *testing.T, directory string) {
	t.Helper()
	path := filepath.Join(directory, "fail-next-forward")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fakeSSHExecutable(t *testing.T, directory string) string {
	t.Helper()
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "ssh")
	script := fmt.Sprintf("#!/bin/sh\nexec %s -test.run='^TestOpenSSHHelperProcess$' -- \"$@\"\n", shellQuote(testExecutable))
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func reservedPort(t *testing.T) (uint16, func()) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return uint16(port), func() { _ = listener.Close() }
}

func waitClosed(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		t.Fatal("forward did not become ready")
	}
}

func waitError(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("forward did not stop")
		return nil
	}
}

func controlOperation(arguments []string) string {
	for index, argument := range arguments {
		if argument == "-M" {
			return "master"
		}
		if argument == "-O" && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	return "session"
}

func controlForward(arguments []string) string {
	for index, argument := range arguments {
		if argument == "-L" && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	return ""
}

func appendEvent(event string) {
	file, err := os.OpenFile("events", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(1)
	}
	_, err = fmt.Fprintln(file, event)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		os.Exit(1)
	}
}

func readEvents(t *testing.T, directory string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "events"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

func countEvent(events []string, want string) int {
	count := 0
	for _, event := range events {
		if event == want {
			count++
		}
	}
	return count
}

func wantForwardLifecycle(t *testing.T, events []string, localPort, remotePort uint16) {
	t.Helper()
	specification := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort)
	if countEvent(events, "forward="+specification) != 1 ||
		countEvent(events, "cancel="+specification) != 1 {
		t.Fatalf("events = %v, want forward and cancel for %s", events, specification)
	}
}
