package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

func TestWriteDoctorReportIncludesFixesAndResult(t *testing.T) {
	var output bytes.Buffer
	report := app.DoctorReport{
		Healthy: false,
		Host:    "dev",
		Checks: []app.DoctorCheck{
			{Name: "openssh", State: app.DoctorOK, Detail: "/usr/bin/ssh"},
			{
				Name: "discovery", State: app.DoctorFailed,
				Detail: "SSH authentication failed", Fix: "Run ssh dev.",
			},
		},
	}
	if err := writeDoctorReport(&output, report); err != nil {
		t.Fatal(err)
	}
	rendered := output.String()
	for _, want := range []string{
		"Host  dev", "OK      openssh", "FAILED  discovery",
		"fix: Run ssh dev.", "Result  needs attention",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("output = %q, missing %q", rendered, want)
		}
	}
}
