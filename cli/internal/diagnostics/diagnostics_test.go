package diagnostics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalogPresentationAndHostSubstitution(t *testing.T) {
	for code := range catalog {
		t.Run(code, func(t *testing.T) {
			require.NotEmpty(t, Text(code))
			require.NotEqual(t, code, Text(code))
			detail, fix := DoctorAdvice(code, "user@dev")
			require.NotEmpty(t, detail)
			require.NotEmpty(t, fix)
			require.NotContains(t, fix, "{host}")
		})
	}
	require.Equal(t, "Run ssh user@dev and verify the configured key or SSH agent.", DoctorFix("authentication_failed", "user@dev"))
	detail, _ := DoctorAdvice("invalid_alias", "dev")
	require.Equal(t, "OpenSSH does not recognize this host alias", detail)
	require.Equal(t, "custom_diagnostic", Text("custom_diagnostic"))
	require.Empty(t, DoctorFix("custom_diagnostic", "dev"))
	detail, fix := DoctorAdvice("custom_diagnostic", "dev")
	require.Equal(t, "SSH connection or remote listener discovery is unavailable", detail)
	require.Equal(t, "Run ssh -v dev to inspect connection details.", fix)
}
