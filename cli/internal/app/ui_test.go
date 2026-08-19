package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWaitForUIReportsDeadChild(t *testing.T) {
	layout := DefaultLayout()
	layout.Dir = t.TempDir()
	layout.UILog = filepath.Join(layout.Dir, "ui.log")
	layout.UIPID = filepath.Join(layout.Dir, "ui.pid")
	layout.UIURL = filepath.Join(layout.Dir, "ui.url")
	if err := os.WriteFile(layout.UILog, []byte("ssh-forward: could not read the running manager: invalid_scope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := WaitForUI(context.Background(), layout, 2*time.Second, 1_000_000)
	if err == nil || !strings.Contains(err.Error(), "invalid_scope") {
		t.Fatalf("WaitForUI err = %v, want the child log line", err)
	}
	if !strings.Contains(err.Error(), strconv.Quote(layout.UILog)) {
		t.Fatalf("WaitForUI err = %v, want a quoted log path", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("WaitForUI took %s, want fail-fast on a dead child", time.Since(start))
	}
}
