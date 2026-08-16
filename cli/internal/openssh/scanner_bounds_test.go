package openssh

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"ssh-forward/cli/internal/core"
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
			t.Fatalf("parseDecimalIdentity(%q) = %q, want invalid non-canonical identity", value, identity)
		}
	}
}

func TestScannerRejectsProcessEvidenceExpansionAcrossListenerEndpoints(t *testing.T) {
	hexText := func(value string) string { return hex.EncodeToString([]byte(value)) }
	var input strings.Builder
	fmt.Fprintf(&input, "SF1\tB\t1\t%s\t%s\tfull\tfull\tfull\t%d\t%d\t%d\t%d\n", hexText("boot"), hexText("net"), MaxObservedListeners, MaxObservedSockets, MaxProcessRecords, MaxObservationMetadataBytes)
	for index := 0; index < MaxObservedListeners; index++ {
		fmt.Fprintf(&input, "SF1\tL\t1\tipv4\tloopback\t%d\t42\n", 10000+index)
	}
	fmt.Fprintf(&input, "SF1\tP\t1\t42\t7\t0\t7\t%s\t%s\t%s\n", hexText("/bin/server"), hexText("/workspace"), hexText("server\x00"))
	input.WriteString("SF1\tE\t1\n")

	var facts []core.SessionFact
	scanObservationFrames(strings.NewReader(input.String()), func(fact core.SessionFact) {
		facts = append(facts, fact)
	})
	for _, fact := range facts {
		if _, ok := fact.(core.ObservationSet); ok {
			t.Fatalf("scanner accepted one inode for %d distinct endpoints", MaxObservedListeners)
		}
	}
	if len(facts) != 1 {
		t.Fatalf("scanner facts = %#v, want one degraded DiscoveryChange", facts)
	}
	change, ok := facts[0].(core.DiscoveryChange)
	if !ok || change.State != core.DiscoveryDegraded || change.Reason != core.ReasonFrameInvalid {
		t.Fatalf("scanner fact = %#v, want degraded invalid frame", facts[0])
	}
}

func TestScannerDowngradesIncompleteProcessChain(t *testing.T) {
	hexText := func(value string) string { return hex.EncodeToString([]byte(value)) }
	input := fmt.Sprintf(
		"SF1\tB\t1\t%s\t%s\tfull\tfull\tfull\t%d\t%d\t%d\t%d\n"+
			"SF1\tL\t1\tipv4\tloopback\t8080\t42\n"+
			"SF1\tP\t1\t42\t7\t1\t6\t%s\t%s\t%s\n"+
			"SF1\tE\t1\n",
		hexText("boot"), hexText("net"), MaxObservedListeners, MaxObservedSockets, MaxProcessRecords, MaxObservationMetadataBytes, hexText("/bin/parent"), hexText("/workspace"), hexText("parent\x00"),
	)
	var set core.ObservationSet
	scanObservationFrames(strings.NewReader(input), func(fact core.SessionFact) {
		if observation, ok := fact.(core.ObservationSet); ok {
			set = observation
		}
	})
	if set.Capability.ProcessMetadata != core.CapabilityPartial {
		t.Fatalf("Process Metadata capability = %q, want partial", set.Capability.ProcessMetadata)
	}
}

func TestScannerCountsUnsupportedObservationBeginsWhileDiscarding(t *testing.T) {
	input := strings.Repeat("SF2\tB\t1\tunsupported\n", 3)
	var changes []core.DiscoveryChange
	scanObservationFrames(strings.NewReader(input), func(fact core.SessionFact) {
		if change, ok := fact.(core.DiscoveryChange); ok {
			changes = append(changes, change)
		}
	})
	if len(changes) != 3 {
		t.Fatalf("Discovery changes = %#v, want three consecutive invalid observations", changes)
	}
	if changes[0].State != core.DiscoveryDegraded || changes[1].State != core.DiscoveryDegraded || changes[2].State != core.DiscoveryFailed {
		t.Fatalf("invalid observation states = %#v, want degraded, degraded, failed", changes)
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
    base_socket_capability=full
}
command() {
    if [ "$1" = -v ] && { [ "$2" = ss ] || [ "$2" = lsof ]; }; then
        return 0
    fi
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
	command := exec.Command("/bin/sh")
	command.Stdin = strings.NewReader(harness)
	output, err := command.CombinedOutput()
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
		t.Fatalf("fallback attempts = %v, want proc, ss, lsof", got)
	}
}

func TestScannerRejectsDeclaredBudgetBeyondFrameLimits(t *testing.T) {
	hexText := func(value string) string { return hex.EncodeToString([]byte(value)) }
	budget := func(listeners, sockets, records, metadata int) string {
		return strings.Join([]string{strconv.Itoa(listeners), strconv.Itoa(sockets), strconv.Itoa(records), strconv.Itoa(metadata)}, "\t")
	}
	for name, frame := range map[string]string{
		"listeners": budget(MaxObservedListeners+1, MaxObservedSockets, MaxProcessRecords, MaxObservationMetadataBytes),
		"sockets":   budget(MaxObservedListeners, MaxObservedSockets+1, MaxProcessRecords, MaxObservationMetadataBytes),
		"processes": budget(MaxObservedListeners, MaxObservedSockets, MaxProcessRecords+1, MaxObservationMetadataBytes),
		"metadata":  budget(MaxObservedListeners, MaxObservedSockets, MaxProcessRecords, MaxObservationMetadataBytes+1),
		"zero":      budget(0, MaxObservedSockets, MaxProcessRecords, MaxObservationMetadataBytes),
	} {
		t.Run(name, func(t *testing.T) {
			input := fmt.Sprintf("SF1\tB\t1\t%s\t%s\tfull\tfull\tfull\t%s\nSF1\tE\t1\n", hexText("boot"), hexText("net"), frame)
			var facts []core.SessionFact
			scanObservationFrames(strings.NewReader(input), func(fact core.SessionFact) {
				facts = append(facts, fact)
			})
			if len(facts) != 1 {
				t.Fatalf("scanner facts = %#v, want one invalid frame change", facts)
			}
			change, ok := facts[0].(core.DiscoveryChange)
			if !ok || change.Reason != core.ReasonFrameInvalid {
				t.Fatalf("scanner fact = %#v, want invalid frame change", facts[0])
			}
		})
	}
}

func TestScannerRejectsRecordsBeyondDeclaredBudget(t *testing.T) {
	hexText := func(value string) string { return hex.EncodeToString([]byte(value)) }
	input := fmt.Sprintf(
		"SF1\tB\t1\t%s\t%s\tfull\tfull\tfull\t2\t1\t1\t128\n"+
			"SF1\tL\t1\tipv4\tloopback\t8080\t42\n"+
			"SF1\tL\t1\tipv6\tloopback\t8080\t43\n"+
			"SF1\tE\t1\n",
		hexText("boot"), hexText("net"),
	)
	var facts []core.SessionFact
	scanObservationFrames(strings.NewReader(input), func(fact core.SessionFact) {
		facts = append(facts, fact)
	})
	if len(facts) != 1 {
		t.Fatalf("scanner facts = %#v, want one invalid frame change", facts)
	}
	if change, ok := facts[0].(core.DiscoveryChange); !ok || change.Reason != core.ReasonFrameInvalid {
		t.Fatalf("scanner fact = %#v, want invalid frame change", facts[0])
	}
}

func TestScannerParsesDeclaredBudget(t *testing.T) {
	hexText := func(value string) string { return hex.EncodeToString([]byte(value)) }
	input := fmt.Sprintf(
		"SF1\tB\t1\t%s\t%s\tfull\tfull\tfull\t%d\t%d\t%d\t%d\n"+
			"SF1\tE\t1\n",
		hexText("boot"), hexText("net"), MaxObservedListeners, MaxObservedSockets, MaxProcessRecords, MaxObservationMetadataBytes,
	)
	var set core.ObservationSet
	scanObservationFrames(strings.NewReader(input), func(fact core.SessionFact) {
		if observation, ok := fact.(core.ObservationSet); ok {
			set = observation
		}
	})
	want := core.ObservationBudget{Listeners: MaxObservedListeners, Sockets: MaxObservedSockets, ProcessRecords: MaxProcessRecords, MetadataBytes: MaxObservationMetadataBytes}
	if !reflect.DeepEqual(set.Budget, want) {
		t.Fatalf("declared Budget = %#v, want %#v", set.Budget, want)
	}
}

func TestScannerListenerRecordLimitDowngradesAvailableEvidence(t *testing.T) {
	prefix, _, found := strings.Cut(scannerScript, "\nwhile :; do\n")
	if !found {
		t.Fatal("scanner main loop marker is missing")
	}
	harness := prefix + `
listener_count=256
listener_capability=full
socket_capability=full
process_capability=full
apply_listener_record_limit
printf '%s|%s|%s\n' "$listener_capability" "$socket_capability" "$process_capability"
process_capability=unavailable
apply_listener_record_limit
printf '%s\n' "$process_capability"
`
	command := exec.Command("/bin/sh")
	command.Stdin = strings.NewReader(harness)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("apply listener record limit: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "partial|partial|partial\nunavailable" {
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
	command := exec.Command("/bin/sh")
	command.Stdin = strings.NewReader(harness)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("read bounded command line: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "partial|8192" {
		t.Fatalf("bounded command line = %q, want partial with 4096 encoded bytes", got)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// The scanner script's declared budgets, the parser's frame caps, and core's
// retention caps are the same protocol default family; a drift between any
// two of them should fail this test rather than silently diverge at runtime.
func TestScannerScriptDeclaresParserDefaultBudgets(t *testing.T) {
	// The parser caps are derived from the embedded script, so script and
	// parser cannot drift. What remains to pin is the second copy: core's
	// retention caps, which are the same protocol defaults and must stay in
	// one numeric family with the script's declarations.
	if MaxObservedListeners != core.MaxRetainedListenerObservations {
		t.Fatalf("parser listener cap %d != core retention cap %d", MaxObservedListeners, core.MaxRetainedListenerObservations)
	}
	if MaxObservedSockets != core.MaxRetainedSocketIdentities {
		t.Fatalf("parser socket cap %d != core retention cap %d", MaxObservedSockets, core.MaxRetainedSocketIdentities)
	}
	if MaxProcessRecords != core.MaxRetainedProcessRecords {
		t.Fatalf("parser process cap %d != core retention cap %d", MaxProcessRecords, core.MaxRetainedProcessRecords)
	}
	if MaxObservationMetadataBytes != core.MaxRetainedProcessMetadataBytes {
		t.Fatalf("parser metadata cap %d != core retention cap %d", MaxObservationMetadataBytes, core.MaxRetainedProcessMetadataBytes)
	}
}
