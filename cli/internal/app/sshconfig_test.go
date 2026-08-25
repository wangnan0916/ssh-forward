package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func writeSSHConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfiguredHostsLiteralsOnly(t *testing.T) {
	dir := t.TempDir()
	path := writeSSHConfig(t, dir, `# personal hosts
Host ubuntu devbox
    User dev

Host *.example.com
    User ignored

Host *
    AddKeysToAgent no

host casesensitive
`)
	hosts, err := ConfiguredHosts(path)
	if err != nil {
		t.Fatalf("ConfiguredHosts: %v", err)
	}
	want := []string{"ubuntu", "devbox", "casesensitive"}
	if diff := cmp.Diff(want, hosts); diff != "" {
		t.Fatalf("hosts mismatch (-want +got):\n%s", diff)
	}
}

func TestConfiguredHostsFollowsInclude(t *testing.T) {
	dir := t.TempDir()
	writeSSHConfig(t, dir, `Host main
`)
	if err := os.MkdirAll(filepath.Join(dir, "conf.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conf.d", "extra.conf"), []byte("Host extra\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The tests run with a real HOME; relative includes resolve under
	// ~/.ssh, so point the test at absolute includes instead.
	absolute := filepath.Join(dir, "conf.d", "extra.conf")
	path := writeSSHConfig(t, dir, `Host first
Include `+absolute+`
Include `+filepath.Join(dir, "conf.d", "*.conf")+`
Include missing-file.conf
`)
	hosts, err := ConfiguredHosts(path)
	if err != nil {
		t.Fatalf("ConfiguredHosts: %v", err)
	}
	want := []string{"first", "extra"}
	if diff := cmp.Diff(want, hosts); diff != "" {
		t.Fatalf("hosts mismatch (-want +got):\n%s", diff)
	}
}

func TestConfiguredHostsMissingFileIsEmpty(t *testing.T) {
	hosts, err := ConfiguredHosts(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("ConfiguredHosts: %v", err)
	}
	if len(hosts) != 0 {
		t.Fatalf("hosts = %v, want none", hosts)
	}
}

func TestConfiguredHostsDeduplicatesAndGuardsCycles(t *testing.T) {
	dir := t.TempDir()
	a := writeSSHConfig(t, dir, "Host a\n")
	loop := writeSSHConfig(t, dir, "Host loop\nInclude "+a+"\n")
	path := writeSSHConfig(t, dir, "Host a\nHost loop\nInclude "+loop+"\n")
	hosts, err := ConfiguredHosts(path)
	if err != nil {
		t.Fatalf("ConfiguredHosts: %v", err)
	}
	want := []string{"a", "loop"}
	if diff := cmp.Diff(want, hosts); diff != "" {
		t.Fatalf("hosts mismatch (-want +got):\n%s", diff)
	}
}
