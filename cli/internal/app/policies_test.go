package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ssh-forward/cli/internal/core"

	"github.com/google/go-cmp/cmp"
)

func strptr(value string) *string { return &value }

func writePolicyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStripJSONC(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "line comments and trailing commas",
			input: `{ // comment` + "\n" + `  "policies": [ {"id": "a",}, ],` + "\n" + `}`,
			want:  "{ \n  \"policies\": [ {\"id\": \"a\"} ]\n}",
		},
		{
			name:  "block comments",
			input: `{ /* block */ "a": /* inner */ 1 }`,
			want:  `{  "a":  1 }`,
		},
		{
			name:  "comment-like text inside strings survives",
			input: `{"executable": "http://x//y/*z*/", "note": "trailing,"}`,
			want:  `{"executable": "http://x//y/*z*/", "note": "trailing,"}`,
		},
		{
			name:  "escaped quote inside string",
			input: `{"a": "say \"// not a comment\""}`,
			want:  `{"a": "say \"// not a comment\""}`,
		},
		{
			name:    "unterminated string",
			input:   `{"a": "oops`,
			wantErr: true,
		},
		{
			name:    "unterminated block comment",
			input:   `{"a": 1} /*`,
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := stripJSONC([]byte(test.input))
			if test.wantErr {
				if err == nil {
					t.Fatalf("stripJSONC(%q) = %q, want error", test.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("stripJSONC(%q): %v", test.input, err)
			}
			if string(got) != test.want {
				t.Fatalf("stripJSONC(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestLoadPoliciesRoundTrip(t *testing.T) {
	path := writePolicyFile(t, `{
  // Frontend development listeners.
  "schema_version": 1,
  "policies": [
    {
      "id": "web-dev",
      "priority": 10,
      "action": "auto_forward",
      "conditions": [
        { "remote_ports": { "from": 8000, "to": 9000 } },
        { "executable": "node" },
        { "working_directory_tree": "/srv/app" },
      ],
    },
    { "id": "db", "priority": 5, "action": "ignore", "conditions": [ { "remote_ports": { "from": 5432, "to": 5432 } } ] },
  ],
}
`)
	policies, err := LoadPolicies(path)
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}
	want := []core.ForwardingPolicy{
		{
			ID:       "web-dev",
			Priority: 10,
			Action:   core.PolicyAutoForward,
			Conditions: []core.PolicyCondition{
				{RemotePorts: &core.PortRange{From: 8000, To: 9000}},
				{Executable: strptr("node")},
				{WorkingDirectoryTree: strptr("/srv/app")},
			},
		},
		{
			ID:         "db",
			Priority:   5,
			Action:     core.PolicyIgnore,
			Conditions: []core.PolicyCondition{{RemotePorts: &core.PortRange{From: 5432, To: 5432}}},
		},
	}
	if diff := cmp.Diff(policies, want); diff != "" {
		t.Fatalf("policies mismatch (-got +want):\n%s", diff)
	}
}

func TestLoadPoliciesRejectsInvalidFiles(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "unknown top-level field", content: `{"schema_version": 1, "policies": [], "surprise": 1}`, wantErr: "unknown field"},
		{name: "unsupported schema", content: `{"schema_version": 2, "policies": []}`, wantErr: "unsupported schema_version"},
		{name: "invalid action", content: `{"schema_version": 1, "policies": [{"id": "a", "action": "forward_now"}]}`, wantErr: "invalid action"},
		{name: "missing id", content: `{"schema_version": 1, "policies": [{"action": "ignore"}]}`, wantErr: "missing id"},
		{name: "inverted port range", content: `{"schema_version": 1, "policies": [{"id": "a", "action": "ignore", "conditions": [{"remote_ports": {"from": 9000, "to": 8000}}]}]}`, wantErr: "inverted"},
		{name: "invalid bind scope", content: `{"schema_version": 1, "policies": [{"id": "a", "action": "ignore", "conditions": [{"bind_scope": "everywhere"}]}]}`, wantErr: "invalid bind_scope"},
		{name: "unknown condition field", content: `{"schema_version": 1, "policies": [{"id": "a", "action": "ignore", "conditions": [{"remote_port": 8080}]}]}`, wantErr: "unknown field"},
		{name: "unterminated comment", content: `{"schema_version": 1, "policies": []} /*`, wantErr: "unterminated block comment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writePolicyFile(t, test.content)
			_, err := LoadPolicies(path)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("LoadPolicies err = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestLoadPoliciesMissingFile(t *testing.T) {
	if _, err := LoadPolicies(filepath.Join(t.TempDir(), "absent.jsonc")); err == nil {
		t.Fatal("LoadPolicies on a missing file succeeded")
	}
}

func TestFilePolicySourceKeepsLastValidOnInvalidEdit(t *testing.T) {
	path := writePolicyFile(t, `{"schema_version": 1, "policies": [{"id": "a", "action": "ignore", "conditions": [{"remote_ports": {"from": 8080, "to": 8080}}]}]}`)
	source := FilePolicySource(path)
	first := source()
	if len(first) != 1 || first[0].ID != "a" {
		t.Fatalf("first read = %#v, want policy a", first)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version": 1, "policies": [{"id": "broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	second := source()
	if diff := cmp.Diff(second, first); diff != "" {
		t.Fatalf("invalid edit changed the source (-second +first):\n%s", diff)
	}
}

func TestMarshalPoliciesUsesFileShape(t *testing.T) {
	policies := []core.ForwardingPolicy{
		{ID: "web", Priority: 10, Action: core.PolicyAutoForward, Conditions: []core.PolicyCondition{
			{RemotePorts: &core.PortRange{From: 8080, To: 8081}},
			{BindScope: &scopeLoopback},
		}},
		{ID: "db", Priority: 5, Action: core.PolicyIgnore},
	}
	encoded, err := MarshalPolicies(policies)
	if err != nil {
		t.Fatalf("MarshalPolicies: %v", err)
	}
	text := string(encoded)
	for _, want := range []string{
		`"schema_version":1`,
		`"id":"web"`,
		`"priority":10`,
		`"action":"auto_forward"`,
		`"remote_ports":{"from":8080,"to":8081}`,
		`"bind_scope":"loopback"`,
		`"id":"db"`,
		`"action":"ignore"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("MarshalPolicies missing %q: %s", want, text)
		}
	}
}

var scopeLoopback = core.BindLoopback

// TestMarshalPoliciesRoundTripsThroughLoad pins the file shape as the one
// contract: what LoadPolicies accepts, MarshalPolicies emits.
func TestMarshalPoliciesRoundTripsThroughLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	content := `{"schema_version": 1, "policies": [
	  {"id": "web", "priority": 10, "action": "auto_forward", "conditions": [{"remote_ports": {"from": 8080, "to": 8080}}]},
	  {"id": "db", "priority": 5, "action": "ignore"}
	]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPolicies(path)
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}
	encoded, err := MarshalPolicies(loaded)
	if err != nil {
		t.Fatalf("MarshalPolicies: %v", err)
	}
	roundPath := filepath.Join(t.TempDir(), "roundtrip.jsonc")
	if err := os.WriteFile(roundPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadPolicies(roundPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if diff := cmp.Diff(loaded, reloaded); diff != "" {
		t.Fatalf("round trip mismatch (-loaded +reloaded):\n%s", diff)
	}
}

func TestFilePolicyReaderKeepsLastValidOnInvalidInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	valid := `{"schema_version": 1, "policies": [{"id": "web", "action": "auto_forward"}]}`
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := NewFilePolicyReader(path)
	source := reader.Source()

	policies := source()
	if len(policies) != 1 || policies[0].ID != "web" {
		t.Fatalf("source after valid read = %#v", policies)
	}

	corrupt := `{"schema_version": 1, "policies": [{"id": "broken", "action": "bogus"}]}`
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := reader.Read()
	if err == nil {
		t.Fatal("Read on a corrupt file succeeded")
	}
	if len(got) != 1 || got[0].ID != "web" {
		t.Fatalf("Read on corrupt file = %#v, want last valid set", got)
	}
	if still := source(); len(still) != 1 || still[0].ID != "web" {
		t.Fatalf("source after corrupt read = %#v, want last valid set", still)
	}

	// The Manager and the CLI share one reader: a fresh parse by the CLI
	// (Read) refreshes the state the Manager's next generation sees.
	fixed := `{"schema_version": 1, "policies": [{"id": "db", "action": "ignore"}]}`
	if err := os.WriteFile(path, []byte(fixed), 0o600); err != nil {
		t.Fatal(err)
	}
	refreshed, err := reader.Read()
	if err != nil || len(refreshed) != 1 || refreshed[0].ID != "db" {
		t.Fatalf("Read after fix = %#v, %v", refreshed, err)
	}
	if fromSource := source(); len(fromSource) != 1 || fromSource[0].ID != "db" {
		t.Fatalf("source after fix = %#v", fromSource)
	}
}

// TestMarshalPoliciesPinsEveryConditionField extends the file-shape
// round trip to the full condition surface: remote ports, bind scope,
// executable, ancestor executable, and working-directory tree must all
// survive core → file → core without drift.
func TestMarshalPoliciesPinsEveryConditionField(t *testing.T) {
	executable := "/usr/local/bin/node"
	ancestor := "/usr/bin/npm"
	tree := "/srv/app"
	scope := "loopback"
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	content := `{"schema_version": 1, "policies": [{"id": "web", "priority": 10, "action": "auto_forward", "conditions": [
	  {"remote_ports": {"from": 8080, "to": 8081}, "bind_scope": "loopback", "executable": "/usr/local/bin/node", "ancestor_executable": "/usr/bin/npm", "working_directory_tree": "/srv/app"}
	]}]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPolicies(path)
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}
	encoded, err := MarshalPolicies(loaded)
	if err != nil {
		t.Fatalf("MarshalPolicies: %v", err)
	}
	roundPath := filepath.Join(t.TempDir(), "roundtrip.jsonc")
	if err := os.WriteFile(roundPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadPolicies(roundPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if diff := cmp.Diff(loaded, reloaded); diff != "" {
		t.Fatalf("round trip mismatch (-loaded +reloaded):\n%s", diff)
	}
	for _, want := range []string{executable, ancestor, tree, scope, `"from":8080`, `"to":8081`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("Marshaled file missing %q: %s", want, string(encoded))
		}
	}
}
