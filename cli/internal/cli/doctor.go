package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

var ErrDoctorFailed = errors.New("doctor found one or more failures")

func (c *doctorCommand) Run(a *App, ctx context.Context) error {
	report := app.Diagnose(ctx, a.Options)
	var err error
	if c.JSON {
		err = a.writeJSON(report)
	} else {
		err = writeDoctorReport(a.Options.Stdout, report)
	}
	if err != nil {
		return err
	}
	if !report.Healthy {
		return ErrDoctorFailed
	}
	return nil
}

func writeDoctorReport(writer io.Writer, report app.DoctorReport) error {
	var output strings.Builder
	output.WriteString("ssh-forward doctor\n")
	if report.Host != "" {
		fmt.Fprintf(&output, "Host  %s\n", report.Host)
	}
	output.WriteByte('\n')
	for _, check := range report.Checks {
		fmt.Fprintf(&output, "%-7s %-11s %s\n", strings.ToUpper(string(check.State)), check.Name, check.Detail)
		if check.Fix != "" {
			fmt.Fprintf(&output, "        fix: %s\n", check.Fix)
		}
	}
	result := "healthy"
	if !report.Healthy {
		result = "needs attention"
	}
	fmt.Fprintf(&output, "\nResult  %s\n", result)
	_, err := io.WriteString(writer, output.String())
	return err
}
