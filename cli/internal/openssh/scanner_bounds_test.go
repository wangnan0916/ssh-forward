package openssh

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestScannerRequiresCanonicalDecimalIdentities(t *testing.T) {
	for _, value := range []string{"0", "42"} {
		identity, err := parseDecimalIdentity(value)
		if err != nil || identity != value {
			t.Fatalf("parseDecimalIdentity(%q) = %q, %v", value, identity, err)
		}
	}
	for _, value := range []string{"00", "042"} {
		if identity, err := parseDecimalIdentity(value); err == nil {
			t.Fatalf("parseDecimalIdentity(%q) = %q, want invalid", value, identity)
		}
	}
}

func TestScannerDowngradesIncompleteProcessChain(t *testing.T) {
	input := fmt.Sprintf(
		"SF1\tB\t1\tfull\tfull\n"+
			"SF1\tL\t1\tipv4\tloopback\t8080\t42\n"+
			"SF1\tP\t1\t42\t7\t1\t6\t%s\t%s\t%s\n"+
			"SF1\tE\t1\n",
		hexText("/bin/parent"), hexText("/workspace"), hexText("parent\x00"),
	)
	set, _ := parseScannerFacts(input)
	if set.Capability.ProcessMetadata != core.CapabilityPartial {
		t.Fatalf("Process Metadata capability = %q, want partial", set.Capability.ProcessMetadata)
	}
}

func TestScannerRejectsReusedInodeAcrossEndpoints(t *testing.T) {
	input := "SF1\tB\t1\tfull\tfull\n" +
		"SF1\tL\t1\tipv4\tloopback\t8080\t42\n" +
		"SF1\tL\t1\tipv4\tloopback\t8081\t42\n" +
		"SF1\tE\t1\n"
	_, facts := parseScannerFacts(input)
	if len(facts) != 1 {
		t.Fatalf("facts = %#v, want one invalid change", facts)
	}
	change, ok := facts[0].(core.DiscoveryChange)
	if !ok || change.State != core.DiscoveryDegraded || change.Reason != core.ReasonObservationInvalid {
		t.Fatalf("fact = %#v, want degraded invalid frame", facts[0])
	}
}

func TestScannerCountsUnsupportedObservations(t *testing.T) {
	_, facts := parseScannerFacts(strings.Repeat("SF2\tB\t1\tunsupported\n", 3))
	if len(facts) != 3 {
		t.Fatalf("facts = %#v, want three changes", facts)
	}
	for index, state := range []core.DiscoveryState{core.DiscoveryDegraded, core.DiscoveryDegraded, core.DiscoveryFailed} {
		if change := facts[index].(core.DiscoveryChange); change.State != state {
			t.Fatalf("change %d = %#v, want %q", index, change, state)
		}
	}
}

func TestScannerFallbackTriesProcThenSSThenLsof(t *testing.T) {
	prefix, _, found := strings.Cut(scannerScript, "\nwhile :; do\n")
	if !found {
		t.Fatal("scanner main loop marker is missing")
	}
	attempts := filepath.Join(t.TempDir(), "attempts")
	harness := prefix + fmt.Sprintf(`
choose_scanner_source() {
    scanner_source=proc
    base_listener_capability=full
}
command() {
    if [ "$1" = -v ] && { [ "$2" = ss ] || [ "$2" = lsof ]; }; then return 0; fi
    return 1
}
scan_listeners() {
    printf '%%s\n' "$scanner_source" >>%s
    case "$scanner_source" in
        proc|ss) return 1 ;;
        lsof) printf 'ipv4\tloopback\t8080\t0\n' ;;
        *) return 1 ;;
    esac
}
choose_scanner_source
scan_current_listeners
printf '%%s|%%s|%%s\n' "$scanner_source" "$scan_status" "$current_listeners"
`, shellQuote(attempts))
	output, err := runShell(harness)
	if err != nil {
		t.Fatalf("exercise scanner fallback: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "lsof|0|ipv4\tloopback\t8080\t0" {
		t.Fatalf("fallback result = %q", got)
	}
	attempted, err := os.ReadFile(attempts)
	if err != nil {
		t.Fatalf("read fallback attempts: %v", err)
	}
	if got := strings.Fields(string(attempted)); fmt.Sprint(got) != "[proc ss lsof]" {
		t.Fatalf("fallback attempts = %v", got)
	}
}

func TestScannerListenerLimitDowngradesCapabilities(t *testing.T) {
	prefix, _, found := strings.Cut(scannerScript, "\nwhile :; do\n")
	if !found {
		t.Fatal("scanner main loop marker is missing")
	}
	output, err := runShell(prefix + `
listener_count=256
listener_capability=full
process_capability=full
apply_listener_record_limit
printf '%s|%s\n' "$listener_capability" "$process_capability"
process_capability=unavailable
apply_listener_record_limit
printf '%s\n' "$process_capability"
`)
	if err != nil {
		t.Fatalf("apply listener limit: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "partial|partial\nunavailable" {
		t.Fatalf("limited capabilities = %q", got)
	}
}

func TestScannerMarksTruncatedCommandLine(t *testing.T) {
	prefix, _, found := strings.Cut(scannerScript, "\nwhile :; do\n")
	if !found {
		t.Fatal("scanner main loop marker is missing")
	}
	path := filepath.Join(t.TempDir(), "cmdline")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxProcessTextBytes+1)), 0o600); err != nil {
		t.Fatalf("write command line fixture: %v", err)
	}
	harness := prefix + fmt.Sprintf(`
if read_process_arguments %s; then status=complete; else status=partial; fi
printf '%%s|%%s\n' "$status" "${#arguments_hex}"
`, shellQuote(path))
	output, err := runShell(harness)
	if err != nil {
		t.Fatalf("read bounded command line: %v\n%s", err, output)
	}
	want := fmt.Sprintf("partial|%d", maxProcessTextBytes*2)
	if got := strings.TrimSpace(string(output)); got != want {
		t.Fatalf("bounded command line = %q, want %q", got, want)
	}
}

func parseScannerFacts(input string) (core.ObservationSet, []core.SessionFact) {
	var set core.ObservationSet
	var facts []core.SessionFact
	scanObservationFrames(strings.NewReader(input), func(fact core.SessionFact) {
		facts = append(facts, fact)
		if observation, ok := fact.(core.ObservationSet); ok {
			set = observation
		}
	})
	return set, facts
}

func hexText(value string) string {
	return hex.EncodeToString([]byte(value))
}

func runShell(script string) ([]byte, error) {
	command := exec.Command("/bin/sh")
	command.Stdin = strings.NewReader(script)
	return command.CombinedOutput()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
