package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ssh-forward/cli/internal/core"
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
	if !reflect.DeepEqual(policies, want) {
		t.Fatalf("policies = %#v, want %#v", policies, want)
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
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("invalid edit changed the source: got %#v, want %#v", second, first)
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
	if !reflect.DeepEqual(loaded, reloaded) {
		t.Fatalf("round trip = %#v, want %#v", reloaded, loaded)
	}
}
