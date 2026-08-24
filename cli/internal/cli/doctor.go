package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

var ErrDoctorFailed = errors.New("doctor found one or more failures")

func (a *App) doctorCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "doctor", Short: "diagnose configuration, SSH, and Manager health", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report := app.Diagnose(cmd.Context(), a.Options)
			if jsonFlag(cmd) {
				if err := a.writeJSON(report); err != nil {
					return err
				}
			} else if err := writeDoctorReport(a.Options.Stdout, report); err != nil {
				return err
			}
			if !report.Healthy {
				return ErrDoctorFailed
			}
			return nil
		},
	}
	command.Flags().Bool("json", false, "emit JSON")
	return annotateSkipManager(grouped(groupDaily, command))
}

func writeDoctorReport(writer io.Writer, report app.DoctorReport) error {
	var output strings.Builder
	output.WriteString("ssh-forward doctor\n")
	if report.Host != "" {
		fmt.Fprintf(&output, "Host  %s\n", report.Host)
	}
	output.WriteByte('\n')
	for _, check := range report.Checks {
		fmt.Fprintf(
			&output, "%-7s %-11s %s\n",
			strings.ToUpper(string(check.State)), check.Name, check.Detail,
		)
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
