package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"ssh-forward/cli/internal/core"
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
	case string(core.PolicyAsk):
		action = core.PolicyAsk
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

// FilePolicySource returns a policy source for the Manager's reconciliation
// seam: each call rereads policies.jsonc (the Manager refreshes on every
// observation generation, roughly the scanner's cadence), so external edits
// take effect without a watcher. Invalid input keeps the last valid set;
// before any valid read the source is empty (default Ask).
func FilePolicySource(path string) func() []core.ForwardingPolicy {
	lastValid := []core.ForwardingPolicy(nil)
	return func() []core.ForwardingPolicy {
		policies, err := LoadPolicies(path)
		if err != nil {
			return lastValid
		}
		lastValid = policies
		return policies
	}
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
