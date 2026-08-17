package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveHostFallsBackToSSHConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_FORWARD_CONFIG_DIR", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host ubuntu\n    User dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host, err := ResolveHost(ResolveOptions{})
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host != "ubuntu" {
		t.Fatalf("host = %q, want ubuntu", host)
	}
}

func TestResolveHostReportsAmbiguousSSHHosts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_FORWARD_CONFIG_DIR", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host ubuntu devbox\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveHost(ResolveOptions{})
	if !IsResolution(err) || err == nil || !strings.Contains(err.Error(), "configured hosts: ubuntu, devbox") {
		t.Fatalf("ResolveHost err = %v, want the candidate list", err)
	}
}

func TestResolveHostInteractivePick(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := t.TempDir()
	t.Setenv("SSH_FORWARD_CONFIG_DIR", configDir)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host ubuntu devbox\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var prompt bytes.Buffer
	host, err := ResolveHost(ResolveOptions{
		Interactive: true,
		Stdin:       strings.NewReader("2\n"),
		Stdout:      &prompt,
	})
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host != "devbox" {
		t.Fatalf("host = %q, want devbox (choice 2)", host)
	}
	if !strings.Contains(prompt.String(), "1) ubuntu") || !strings.Contains(prompt.String(), "2) devbox") {
		t.Fatalf("prompt = %q, want the numbered list", prompt.String())
	}
	if _, err := LoadConfig(DefaultLayout().Config); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config.jsonc after the pick = %v, want it untouched", err)
	}
}

func TestResolveHostInteractiveRejectsGarbage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_FORWARD_CONFIG_DIR", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host ubuntu devbox\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var prompt bytes.Buffer
	host, err := ResolveHost(ResolveOptions{
		Interactive: true,
		Stdin:       strings.NewReader("nope\n1\n"),
		Stdout:      &prompt,
	})
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host != "ubuntu" {
		t.Fatalf("host = %q, want ubuntu after retry", host)
	}
	if !strings.Contains(prompt.String(), "invalid choice") {
		t.Fatalf("prompt = %q, want the invalid-choice retry", prompt.String())
	}
}

func TestResolveHostFlagWins(t *testing.T) {
	host, err := ResolveHost(ResolveOptions{HostFlag: "devbox"})
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host != "devbox" {
		t.Fatalf("host = %q, want devbox", host)
	}
}

func TestSSHConfigPathUsesFlag(t *testing.T) {
	if got := SSHConfigPath("/explicit/config"); got != "/explicit/config" {
		t.Fatalf("SSHConfigPath = %q", got)
	}
}
