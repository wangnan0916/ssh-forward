package diagnostics

import "testing"

func TestText(t *testing.T) {
	tests := map[string]string{
		"invalid_alias":            "SSH does not know this host alias.",
		"authentication_failed":    "SSH authentication failed.",
		"host_key_failed":          "SSH host key verification failed.",
		"local_port_conflict":      "the same local port is already in use",
		"local_port_reserved":      "the local port is reserved by a published forward",
		"remote_port_unavailable":  "the Development Host port could not be opened",
		"remote_bind_not_loopback": "the Development Host forced the published port onto a non-loopback address",
		"remote_bind_unverified":   "the Development Host loopback bind could not be verified",
		"transport_unavailable":    "SSH connection unavailable",
		"discovery_invalid":        "the remote listener scan returned invalid data",
		"forward_start_timeout":    "OpenSSH did not open the local port in time",
		"master_start_timeout":     "the shared OpenSSH connection did not become ready in time",
		"custom_diagnostic":        "custom_diagnostic",
	}
	for diagnostic, want := range tests {
		if got := Text(diagnostic); got != want {
			t.Errorf("Text(%q) = %q, want %q", diagnostic, got, want)
		}
	}
}

func TestDoctorAdvice(t *testing.T) {
	tests := map[string][2]string{
		"invalid_alias": {
			"OpenSSH does not recognize this host alias",
			"Use a literal Host alias from the selected OpenSSH config.",
		},
		"authentication_failed": {
			"SSH authentication failed",
			"Run ssh dev and verify the configured key or SSH agent.",
		},
		"discovery_invalid": {
			"the remote listener scan returned invalid data",
			"Verify that the remote host is Linux and exposes readable procfs listener state.",
		},
		"master_start_timeout": {
			"the shared OpenSSH connection did not become ready in time",
			"Run ssh -v dev to inspect connection details.",
		},
		"local_port_reserved": {
			"the local port is reserved by a published forward",
			"Choose another --local port or remove one intent.",
		},
		"remote_port_unavailable": {
			"the Development Host port could not be opened",
			"Check whether the remote port is occupied and whether sshd allows TCP forwarding.",
		},
		"remote_bind_not_loopback": {
			"the Development Host forced the published port onto a non-loopback address",
			"Set sshd GatewayPorts to no or clientspecified before publishing local services.",
		},
		"custom_diagnostic": {
			"SSH connection or remote listener discovery is unavailable",
			"Run ssh -v dev to inspect connection details.",
		},
	}
	for diagnostic, want := range tests {
		detail, fix := DoctorAdvice(diagnostic, "dev")
		if detail != want[0] || fix != want[1] {
			t.Errorf("DoctorAdvice(%q) = %q, %q; want %q, %q", diagnostic, detail, fix, want[0], want[1])
		}
	}
}
