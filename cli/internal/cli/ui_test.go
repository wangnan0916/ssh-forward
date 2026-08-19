package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func uiTestApp(t *testing.T, manager core.Manager) *App {
	t.Helper()
	t.Setenv("SSH_FORWARD_UI_NO_OPEN", "1")
	t.Setenv("SSH_FORWARD_CONFIG_DIR", t.TempDir())
	layout := app.DefaultLayout()
	return &App{
		Manager: manager,
		Host:    core.HostAlias("development"),
		Options: app.Options{
			Layout:       layout,
			PoliciesPath: layout.Policies,
			HostFlag:     "development",
		},
	}
}

func waitUIURL(t *testing.T, layout app.Layout) string {
	t.Helper()
	url, err := waitForUI(context.Background(), layout, 2*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	return url
}

func TestWaitForUIReportsDeadChild(t *testing.T) {
	layout := app.DefaultLayout()
	layout.Dir = t.TempDir()
	layout.UILog = filepath.Join(layout.Dir, "ui.log")
	layout.UIPID = filepath.Join(layout.Dir, "ui.pid")
	layout.UIURL = filepath.Join(layout.Dir, "ui.url")
	if err := os.WriteFile(layout.UILog, []byte("ssh-forward: could not read the running manager: invalid_scope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := waitForUI(context.Background(), layout, 2*time.Second, 1_000_000)
	if err == nil || !strings.Contains(err.Error(), "invalid_scope") {
		t.Fatalf("waitForUI err = %v, want the child log line", err)
	}
	if !strings.Contains(err.Error(), strconv.Quote(layout.UILog)) {
		t.Fatalf("waitForUI err = %v, want a quoted log path", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("waitForUI took %s, want fail-fast on a dead child", time.Since(start))
	}
}

func TestUINeedsSubcommand(t *testing.T) {
	_, err := runCLI(t, uiTestApp(t, nil), "ui")
	if err == nil || !errors.Is(err, ErrUsage) {
		t.Fatalf("ui without subcommand err = %v, want usage", err)
	}
	if !strings.Contains(err.Error(), "start, status, stop") {
		t.Fatalf("ui error = %v", err)
	}
}

func TestUIHelpListsStartStatusStop(t *testing.T) {
	output, err := runCLI(t, uiTestApp(t, nil), "ui", "--help")
	if err != nil {
		t.Fatalf("ui --help: %v", err)
	}
	for _, want := range []string{"start", "status", "stop"} {
		if !strings.Contains(output, want) {
			t.Fatalf("ui --help missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "run the WebUI in the foreground") {
		t.Fatalf("ui --help listed hidden serve:\n%s", output)
	}
}

func TestUIStatusAndStopWhenNotRunning(t *testing.T) {
	surface := uiTestApp(t, nil)
	if _, err := runCLI(t, surface, "ui", "status"); err == nil || !strings.Contains(err.Error(), "WebUI is not running") {
		t.Fatalf("status err = %v, want not running", err)
	}
	if _, err := runCLI(t, surface, "ui", "stop"); err == nil || !strings.Contains(err.Error(), "WebUI is not running") {
		t.Fatalf("stop err = %v, want not running", err)
	}
}

func TestUIStartReprintsLiveURL(t *testing.T) {
	surface := uiTestApp(t, nil)
	url := "http://127.0.0.1:9/?token=already-running"
	if err := os.WriteFile(surface.Options.Layout.UIPID, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(surface.Options.Layout.UIURL, []byte(url+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, surface, "ui", "start")
	if err != nil {
		t.Fatalf("ui start: %v", err)
	}
	if strings.TrimSpace(output) != url {
		t.Fatalf("start output = %q, want the live URL", output)
	}
	jsonOut, err := runCLI(t, surface, "ui", "status", "--json")
	if err != nil {
		t.Fatalf("ui status --json: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonOut)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["url"] != url {
		t.Fatalf("status --json = %q, want %q", jsonOut, url)
	}
}

func TestUIStopTerminatesProcess(t *testing.T) {
	surface := uiTestApp(t, nil)
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	if err := os.WriteFile(surface.Options.Layout.UIPID, []byte(fmt.Sprintf("%d\n", child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(surface.Options.Layout.UIURL, []byte("http://127.0.0.1:9/?token=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, surface, "ui", "stop")
	if err != nil {
		t.Fatalf("ui stop: %v", err)
	}
	if output != "stopped\n" {
		t.Fatalf("stop output = %q", output)
	}
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("stop left the process running")
	}
}

func TestUIServeHTTP(t *testing.T) {
	surface := uiTestApp(t, &fakeManager{snapshot: snapshotWithHost()})
	layout := surface.Options.Layout
	policiesPath := surface.Options.PoliciesPath
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- surface.Run(ctx, []string{"ui", "serve"}) }()
	url := waitUIURL(t, layout)
	page, query, ok := strings.Cut(url, "?")
	if !ok {
		t.Fatalf("url = %q", url)
	}
	api := strings.TrimSuffix(page, "/")
	res, err := http.Get(api + "/api/snapshot?" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d", res.StatusCode)
	}
	body := new(bytes.Buffer)
	if _, err := body.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.String(), `"alias":"development"`) {
		t.Fatalf("snapshot = %s", body.Bytes())
	}

	remember, err := http.Post(api+"/api/remember?"+query, "application/json", strings.NewReader(`{"port":5173}`))
	if err != nil {
		t.Fatal(err)
	}
	defer remember.Body.Close()
	if remember.StatusCode != http.StatusOK {
		t.Fatalf("remember status = %d", remember.StatusCode)
	}
	policies, err := app.LoadPolicies(policiesPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := core.SimpleAutoForwardPorts(policies); len(got) != 1 || got[0] != 5173 {
		t.Fatalf("policies ports = %v", got)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("ui serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ui serve did not stop")
	}
}
