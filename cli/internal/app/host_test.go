package app

import (
	"bytes"
	"errors"
	"io"
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
	host, err := ResolveHost(Options{})
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
	_, err := ResolveHost(Options{})
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
	host, err := ResolveHost(Options{
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
	if !strings.Contains(prompt.String(), "pick one to set as the default") {
		t.Fatalf("prompt = %q, want the pin explanation", prompt.String())
	}
	if !strings.Contains(prompt.String(), "default host set to devbox") {
		t.Fatalf("prompt = %q, want the pin confirmation", prompt.String())
	}
	config, err := LoadConfig(DefaultLayout().Config)
	if err != nil {
		t.Fatalf("config.jsonc after the pick: %v", err)
	}
	if config.DefaultHost != "devbox" {
		t.Fatalf("default_host = %q, want the picked alias", config.DefaultHost)
	}
}

func TestResolveHostInteractivePickByAlias(t *testing.T) {
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
	host, err := ResolveHost(Options{
		Interactive: true,
		Stdin:       strings.NewReader("devbox\n"),
		Stdout:      &prompt,
	})
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host != "devbox" {
		t.Fatalf("host = %q, want the typed alias", host)
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
	host, err := ResolveHost(Options{
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

func TestResolveHostUsesPickHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_FORWARD_CONFIG_DIR", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host ubuntu devbox\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host, err := ResolveHost(Options{
		PickHost: func(hosts []string, stdin io.Reader, stdout io.Writer) (string, error) {
			if len(hosts) != 2 || hosts[0] != "ubuntu" || hosts[1] != "devbox" {
				t.Fatalf("picker hosts = %v, want ubuntu then devbox", hosts)
			}
			return "devbox", nil
		},
	})
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host != "devbox" {
		t.Fatalf("host = %q, want the picker result", host)
	}
	if _, err := LoadConfig(DefaultLayout().Config); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config.jsonc after an injected picker = %v, want it untouched", err)
	}
}

func TestResolveHostFlagWins(t *testing.T) {
	host, err := ResolveHost(Options{HostFlag: "devbox"})
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host != "devbox" {
		t.Fatalf("host = %q, want devbox", host)
	}
}

func TestResolveHostUsesLayoutConfig(t *testing.T) {
	directory := t.TempDir()
	if err := SetDefaultHost(filepath.Join(directory, "config.jsonc"), "layout-dev"); err != nil {
		t.Fatal(err)
	}
	host, err := ResolveHost(Options{Layout: Layout{Dir: directory}})
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host != "layout-dev" {
		t.Fatalf("host = %q, want layout-dev", host)
	}
}

func TestSSHConfigPathUsesFlag(t *testing.T) {
	if got := SSHConfigPath("/explicit/config"); got != "/explicit/config" {
		t.Fatalf("SSHConfigPath = %q", got)
	}
}
