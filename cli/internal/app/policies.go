package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// policiesSchemaVersion is the versioned schema of policies.jsonc
// (ADR-0005): the manager refuses files that declare a different version,
// so the format can evolve without silent misinterpretation.
const policiesSchemaVersion = 1

// policyFile is the on-disk shape of policies.jsonc.
type policyFile struct {
	SchemaVersion int          `json:"schema_version"`
	Policies      []filePolicy `json:"policies"`
}

type filePolicy struct {
	ID         string          `json:"id"`
	Priority   int             `json:"priority"`
	Action     string          `json:"action"`
	Conditions []fileCondition `json:"conditions"`
}

type fileCondition struct {
	RemotePorts          *filePortRange `json:"remote_ports,omitempty"`
	BindScope            *string        `json:"bind_scope,omitempty"`
	Executable           *string        `json:"executable,omitempty"`
	AncestorExecutable   *string        `json:"ancestor_executable,omitempty"`
	WorkingDirectoryTree *string        `json:"working_directory_tree,omitempty"`
}

type filePortRange struct {
	From uint16 `json:"from"`
	To   uint16 `json:"to"`
}

// LoadPolicies reads and validates a policies.jsonc file. Invalid input
// (bad JSONC, unknown fields, invalid enums, inverted port ranges) is
// rejected wholesale: the file is the single source of truth and a corrupt
// file must not silently drop policies.
func LoadPolicies(path string) ([]core.ForwardingPolicy, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	plain, err := stripJSONC(content)
	if err != nil {
		return nil, err
	}
	var file policyFile
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("policies.jsonc: %w", err)
	}
	if file.SchemaVersion != policiesSchemaVersion {
		return nil, fmt.Errorf("policies.jsonc: unsupported schema_version %d (want %d)", file.SchemaVersion, policiesSchemaVersion)
	}
	policies := make([]core.ForwardingPolicy, 0, len(file.Policies))
	for _, entry := range file.Policies {
		policy, err := translatePolicy(entry)
		if err != nil {
			return nil, fmt.Errorf("policy %q: %w", entry.ID, err)
		}
		policies = append(policies, policy)
	}
	return policies, nil
}

func translatePolicy(entry filePolicy) (core.ForwardingPolicy, error) {
	if entry.ID == "" {
		return core.ForwardingPolicy{}, errors.New("missing id")
	}
	var action core.PolicyAction
	switch entry.Action {
	case string(core.PolicyAutoForward):
		action = core.PolicyAutoForward
	case string(core.PolicyIgnore):
		action = core.PolicyIgnore
	default:
		return core.ForwardingPolicy{}, fmt.Errorf("invalid action %q", entry.Action)
	}
	conditions := make([]core.PolicyCondition, 0, len(entry.Conditions))
	for _, fileCondition := range entry.Conditions {
		condition, err := translateCondition(fileCondition)
		if err != nil {
			return core.ForwardingPolicy{}, err
		}
		conditions = append(conditions, condition)
	}
	return core.ForwardingPolicy{
		ID:         entry.ID,
		Priority:   entry.Priority,
		Action:     action,
		Conditions: conditions,
	}, nil
}

func translateCondition(entry fileCondition) (core.PolicyCondition, error) {
	var condition core.PolicyCondition
	if entry.RemotePorts != nil {
		if entry.RemotePorts.To < entry.RemotePorts.From {
			return core.PolicyCondition{}, fmt.Errorf("remote_ports %d..%d is inverted", entry.RemotePorts.From, entry.RemotePorts.To)
		}
		condition.RemotePorts = &core.PortRange{From: entry.RemotePorts.From, To: entry.RemotePorts.To}
	}
	if entry.BindScope != nil {
		switch *entry.BindScope {
		case string(core.BindLoopback):
			scope := core.BindLoopback
			condition.BindScope = &scope
		case string(core.BindWildcard):
			scope := core.BindWildcard
			condition.BindScope = &scope
		default:
			return core.PolicyCondition{}, fmt.Errorf("invalid bind_scope %q", *entry.BindScope)
		}
	}
	condition.Executable = entry.Executable
	condition.AncestorExecutable = entry.AncestorExecutable
	condition.WorkingDirectoryTree = entry.WorkingDirectoryTree
	return condition, nil
}

// MarshalPolicies encodes policies in the policies.jsonc file shape
// (ADR-0005): the versioned file schema is the single contract for the
// policy file, the CLI's --json output, and the desktop client.
func MarshalPolicies(policies []core.ForwardingPolicy) ([]byte, error) {
	file := policyFile{
		SchemaVersion: policiesSchemaVersion,
		Policies:      make([]filePolicy, 0, len(policies)),
	}
	for _, policy := range policies {
		file.Policies = append(file.Policies, reversePolicy(policy))
	}
	return json.Marshal(file)
}

func reversePolicy(policy core.ForwardingPolicy) filePolicy {
	conditions := make([]fileCondition, 0, len(policy.Conditions))
	for _, condition := range policy.Conditions {
		conditions = append(conditions, reverseCondition(condition))
	}
	return filePolicy{
		ID:         policy.ID,
		Priority:   policy.Priority,
		Action:     string(policy.Action),
		Conditions: conditions,
	}
}

func reverseCondition(condition core.PolicyCondition) fileCondition {
	var out fileCondition
	if condition.RemotePorts != nil {
		out.RemotePorts = &filePortRange{From: condition.RemotePorts.From, To: condition.RemotePorts.To}
	}
	if condition.BindScope != nil {
		scope := string(*condition.BindScope)
		out.BindScope = &scope
	}
	out.Executable = condition.Executable
	out.AncestorExecutable = condition.AncestorExecutable
	out.WorkingDirectoryTree = condition.WorkingDirectoryTree
	return out
}

// FilePolicySource returns a policy source for the Manager's reconciliation
// seam: each call rereads policies.jsonc (the Manager refreshes on every
// FilePolicyReader is the single read path for the policies file: the
// Manager's reconciliation source and the CLI's diagnostic read share one
// last-valid set and one last error, so both surfaces see the same truth.
type FilePolicyReader struct {
	path string

	mu        sync.Mutex
	lastValid []core.ForwardingPolicy
	lastErr   error
}

func NewFilePolicyReader(path string) *FilePolicyReader {
	return &FilePolicyReader{path: path}
}

// Read parses the file afresh and records the result: the parsed set when
// the file is valid, otherwise the last valid set plus the error. Every
// read path (Source and the CLI) goes through this one place.
func (r *FilePolicyReader) Read() ([]core.ForwardingPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	policies, err := LoadPolicies(r.path)
	if err != nil {
		r.lastErr = err
		return r.lastValid, err
	}
	r.lastValid = policies
	r.lastErr = nil
	return policies, nil
}

// Source returns the Manager's reconciliation seam: each call reads the
// file and returns the last valid set — empty before any valid read
// (unmatched listeners are not forwarded). The reconciliation cadence (~2s per observation
// generation) hot-reloads external edits without a watcher.
func (r *FilePolicyReader) Source() func() []core.ForwardingPolicy {
	return func() []core.ForwardingPolicy {
		policies, _ := r.Read()
		return policies
	}
}

// FilePolicySource builds the Manager's policy source from a file, keeping
// the last valid set on invalid input. Production wiring composes a
// FilePolicyReader instead so the CLI can share the same reader; this
// remains for tests and callers that want only the source seam.
func FilePolicySource(path string) func() []core.ForwardingPolicy {
	return NewFilePolicyReader(path).Source()
}

const cliPolicyPriority = 10

var (
	// ErrEmptyDirectory is returned when add/remove --dir is blank.
	ErrEmptyDirectory = errors.New("directory is empty")
	// ErrHostDirectory is returned when --dir is not an absolute
	// Development Host path (it must start with /).
	ErrHostDirectory = errors.New("directory must be an absolute path on the Development Host")
)

// SavePolicies writes policies.jsonc atomically in the file shape.
func SavePolicies(path string, policies []core.ForwardingPolicy) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	encoded, err := MarshalPolicies(policies)
	if err != nil {
		return err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, encoded, "", "  "); err != nil {
		return err
	}
	indented.WriteByte('\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".policies-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(indented.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func loadPoliciesOrEmpty(path string) ([]core.ForwardingPolicy, error) {
	policies, err := LoadPolicies(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return policies, err
}

// AddAutoForwardPort remembers a remote port: when a service listens there,
// it is forwarded. Returns false when that port rule already exists.
func AddAutoForwardPort(path string, port uint16) (bool, error) {
	policies, err := loadPoliciesOrEmpty(path)
	if err != nil {
		return false, err
	}
	if portAutoForwardIndex(policies, port) >= 0 {
		return false, nil
	}
	policies = append(policies, core.ForwardingPolicy{
		ID:       fmt.Sprintf("port-%d", port),
		Priority: cliPolicyPriority,
		Action:   core.PolicyAutoForward,
		Conditions: []core.PolicyCondition{{
			RemotePorts: &core.PortRange{From: port, To: port},
		}},
	})
	return true, SavePolicies(path, policies)
}

// AddAutoForwardDir remembers a Development Host working-directory tree:
// listeners whose process cwd is in that tree are forwarded. The returned
// string is the stored path (trailing slashes stripped except for "/").
func AddAutoForwardDir(path, dir string) (bool, string, error) {
	dir, err := normalizeHostDir(dir)
	if err != nil {
		return false, "", err
	}
	policies, err := loadPoliciesOrEmpty(path)
	if err != nil {
		return false, "", err
	}
	if dirAutoForwardIndex(policies, dir) >= 0 {
		return false, dir, nil
	}
	policies = append(policies, core.ForwardingPolicy{
		ID:       "dir-" + dir,
		Priority: cliPolicyPriority,
		Action:   core.PolicyAutoForward,
		Conditions: []core.PolicyCondition{{
			WorkingDirectoryTree: &dir,
		}},
	})
	return true, dir, SavePolicies(path, policies)
}

// RemoveAutoForwardPort drops the simple port Auto-forward written by add.
func RemoveAutoForwardPort(path string, port uint16) (bool, error) {
	policies, err := loadPoliciesOrEmpty(path)
	if err != nil {
		return false, err
	}
	index := portAutoForwardIndex(policies, port)
	if index < 0 {
		return false, nil
	}
	policies = append(policies[:index], policies[index+1:]...)
	return true, SavePolicies(path, policies)
}

// RemoveAutoForwardDir drops the simple directory Auto-forward written by add.
func RemoveAutoForwardDir(path, dir string) (bool, string, error) {
	dir, err := normalizeHostDir(dir)
	if err != nil {
		return false, "", err
	}
	policies, err := loadPoliciesOrEmpty(path)
	if err != nil {
		return false, "", err
	}
	index := dirAutoForwardIndex(policies, dir)
	if index < 0 {
		return false, dir, nil
	}
	policies = append(policies[:index], policies[index+1:]...)
	return true, dir, SavePolicies(path, policies)
}

func normalizeHostDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", ErrEmptyDirectory
	}
	if !strings.HasPrefix(dir, "/") {
		return "", ErrHostDirectory
	}
	if dir != "/" {
		dir = strings.TrimSuffix(dir, "/")
	}
	return dir, nil
}

func portAutoForwardIndex(policies []core.ForwardingPolicy, port uint16) int {
	for index, policy := range policies {
		if !simpleAutoForward(policy) {
			continue
		}
		ports := policy.Conditions[0].RemotePorts
		if ports != nil && ports.From == port && ports.To == port {
			return index
		}
	}
	return -1
}

func dirAutoForwardIndex(policies []core.ForwardingPolicy, dir string) int {
	for index, policy := range policies {
		if !simpleAutoForward(policy) {
			continue
		}
		tree := policy.Conditions[0].WorkingDirectoryTree
		if tree != nil && *tree == dir {
			return index
		}
	}
	return -1
}

func simpleAutoForward(policy core.ForwardingPolicy) bool {
	if policy.Action != core.PolicyAutoForward || len(policy.Conditions) != 1 {
		return false
	}
	condition := policy.Conditions[0]
	portOnly := condition.RemotePorts != nil && condition.WorkingDirectoryTree == nil
	dirOnly := condition.WorkingDirectoryTree != nil && condition.RemotePorts == nil
	if !portOnly && !dirOnly {
		return false
	}
	return condition.BindScope == nil && condition.Executable == nil && condition.AncestorExecutable == nil
}

// stripJSONC removes line comments, block comments, and trailing commas
// while staying string-aware, converting the JSONC dialect (ADR-0005) to
// plain JSON. Strings keep their contents verbatim, including comment-like
// sequences.
func stripJSONC(input []byte) ([]byte, error) {
	output := make([]byte, 0, len(input))
	inString := false
	inLineComment := false
	inBlockComment := false
	for index := 0; index < len(input); index++ {
		current := input[index]
		switch {
		case inLineComment:
			if current == '\n' {
				inLineComment = false
				output = append(output, current)
			}
			continue
		case inBlockComment:
			if current == '*' && index+1 < len(input) && input[index+1] == '/' {
				inBlockComment = false
				index++
			}
			continue
		case inString:
			output = append(output, current)
			if current == '\\' && index+1 < len(input) {
				index++
				output = append(output, input[index])
				continue
			}
			if current == '"' {
				inString = false
			}
			continue
		}
		switch current {
		case '"':
			inString = true
			output = append(output, current)
		case '/':
			if index+1 < len(input) && input[index+1] == '/' {
				inLineComment = true
				index++
			} else if index+1 < len(input) && input[index+1] == '*' {
				inBlockComment = true
				index++
			} else {
				output = append(output, current)
			}
		case ',':
			// Drop a comma when the next non-whitespace character closes
			// the array or object: trailing commas are legal JSONC.
			next := index + 1
			for next < len(input) && (input[next] == ' ' || input[next] == '\t' || input[next] == '\n' || input[next] == '\r') {
				next++
			}
			if next < len(input) && (input[next] == ']' || input[next] == '}') {
				continue
			}
			output = append(output, current)
		default:
			output = append(output, current)
		}
	}
	if inString {
		return nil, errors.New("policies.jsonc: unterminated string")
	}
	if inBlockComment {
		return nil, errors.New("policies.jsonc: unterminated block comment")
	}
	return output, nil
}
