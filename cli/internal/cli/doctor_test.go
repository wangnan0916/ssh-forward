package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

func TestWriteDoctorReportIncludesFixesAndResult(t *testing.T) {
	var output bytes.Buffer
	report := app.DoctorReport{
		Healthy: false,
		Host:    "dev",
		Checks: []app.DoctorCheck{
			{Name: "openssh", State: app.DoctorOK, Detail: "/usr/bin/ssh"},
			{Name: "discovery", State: app.DoctorFailed, Detail: "SSH authentication failed", Fix: "Run ssh dev."},
		},
	}
	require.NoError(t, writeDoctorReport(&output, report))
	rendered := output.String()
	for _, want := range []string{"Host  dev", "OK      openssh", "FAILED  discovery", "fix: Run ssh dev.", "Result  needs attention"} {
		require.Contains(t, rendered, want)
	}
}
