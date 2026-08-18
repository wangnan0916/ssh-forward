package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	// policiesSchemaVersion is the versioned schema of policies.jsonc
	// (ADR-0005): the manager refuses files that declare a different version,
	// so the format can evolve without silent misinterpretation.
	policiesSchemaVersion = 1
	// policyFileInvalid is the Snapshot Policy Diagnostic when the policies
	// file exists but cannot be parsed. A missing file is empty, not invalid.
	policyFileInvalid = "policies_file_invalid"
)

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

// FilePolicyReader is the single read-and-mutate path for the policies
// file: the Manager's reconciliation source, Remembered Auto-forward
// writes, and the CLI's policy list share one last-valid set and one last
// error, so every surface sees the same truth.
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

// Source is the Manager's reconciliation seam: each call reads the file
// and returns the last valid set plus a Snapshot diagnostic when the file
// is unreadable. Missing files are empty with no diagnostic. Unmatched
// listeners are not forwarded. The Manager's 250ms policy poll hot-reloads
// external edits without a watcher.
func (r *FilePolicyReader) Source() ([]core.ForwardingPolicy, string) {
	policies, err := r.Read()
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return policies, ""
	}
	return policies, policyFileInvalid
}

func (r *FilePolicyReader) update(apply func([]core.ForwardingPolicy) ([]core.ForwardingPolicy, bool, error)) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	policies, err := LoadPolicies(r.path)
	if errors.Is(err, os.ErrNotExist) {
		policies = nil
	} else if err != nil {
		r.lastErr = err
		return false, err
	}
	updated, changed, err := apply(policies)
	if err != nil {
		return false, err
	}
	r.lastValid = policies
	r.lastErr = nil
	if !changed {
		return false, nil
	}
	if err := SavePolicies(r.path, updated); err != nil {
		return false, err
	}
	r.lastValid = updated
	return true, nil
}

func (r *FilePolicyReader) AddPort(port uint16) (bool, error) {
	return r.update(func(policies []core.ForwardingPolicy) ([]core.ForwardingPolicy, bool, error) {
		updated, changed := core.RememberPort(policies, port)
		return updated, changed, nil
	})
}

func (r *FilePolicyReader) RemovePort(port uint16) (bool, error) {
	return r.update(func(policies []core.ForwardingPolicy) ([]core.ForwardingPolicy, bool, error) {
		updated, changed := core.ForgetPort(policies, port)
		return updated, changed, nil
	})
}

func (r *FilePolicyReader) AddDir(dir string) (bool, string, error) {
	var stored string
	changed, err := r.update(func(policies []core.ForwardingPolicy) ([]core.ForwardingPolicy, bool, error) {
		updated, path, changed, err := core.RememberDirectory(policies, dir)
		stored = path
		return updated, changed, err
	})
	return changed, stored, err
}

func (r *FilePolicyReader) RemoveDir(dir string) (bool, string, error) {
	var stored string
	changed, err := r.update(func(policies []core.ForwardingPolicy) ([]core.ForwardingPolicy, bool, error) {
		updated, path, changed, err := core.ForgetDirectory(policies, dir)
		stored = path
		return updated, changed, err
	})
	return changed, stored, err
}

var (
	// ErrEmptyDirectory and ErrHostDirectory are the Remembered Auto-forward
	// directory errors; the file adapter re-exports the core values so CLI
	// tests can match them without importing identity helpers.
	ErrEmptyDirectory = core.ErrEmptyDirectory
	ErrHostDirectory  = core.ErrHostDirectory
)

// SavePolicies writes policies.jsonc atomically in the file shape.
func SavePolicies(path string, policies []core.ForwardingPolicy) error {
	encoded, err := MarshalPolicies(policies)
	if err != nil {
		return err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, encoded, "", "  "); err != nil {
		return err
	}
	indented.WriteByte('\n')
	return writeAtomic(path, indented.Bytes(), ".policies-*.tmp")
}
