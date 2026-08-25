// Package diagnostics maps bounded backend diagnostics to human-readable text.
package diagnostics

import "strings"

type entry struct {
	text       string
	doctorText string
	doctorFix  string
}

var catalog = map[string]entry{
	"invalid_alias": {
		text:       "SSH does not know this host alias.",
		doctorText: "OpenSSH does not recognize this host alias",
		doctorFix:  "Use a literal Host alias from the selected OpenSSH config.",
	},
	"authentication_failed": {
		text:      "SSH authentication failed.",
		doctorFix: "Run ssh {host} and verify the configured key or SSH agent.",
	},
	"host_key_failed": {
		text:      "SSH host key verification failed.",
		doctorFix: "Run ssh {host} and review the host key warning.",
	},
	"local_port_conflict": {
		text: "the same local port is already in use",
	},
	"transport_unavailable": {
		text: "SSH connection unavailable",
	},
	"discovery_invalid": {
		text:      "the remote listener scan returned invalid data",
		doctorFix: "Verify that the remote host is Linux and exposes readable procfs listener state.",
	},
	"forward_start_timeout": {
		text: "OpenSSH did not open the local port in time",
	},
	"master_start_timeout": {
		text:      "the shared OpenSSH connection did not become ready in time",
		doctorFix: "Run ssh -v {host} to inspect connection details.",
	},
}

func Text(diagnostic string) string {
	if entry, found := catalog[diagnostic]; found {
		return entry.text
	}
	return diagnostic
}

func DoctorAdvice(diagnostic, host string) (string, string) {
	entry, found := catalog[diagnostic]
	if !found || entry.doctorFix == "" {
		return "SSH connection or remote listener discovery is unavailable",
			"Run ssh -v " + host + " to inspect connection details."
	}
	detail := entry.doctorText
	if detail == "" {
		detail = strings.TrimSuffix(entry.text, ".")
	}
	return detail, strings.ReplaceAll(entry.doctorFix, "{host}", host)
}
