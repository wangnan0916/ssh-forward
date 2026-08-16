package core

import (
	"path"
	"slices"
	"strings"
)

// PolicyAction is the decision a Forwarding Policy yields for a Listener
// Observation: forward it, ask the user, or ignore it.
type PolicyAction string

const (
	PolicyAutoForward PolicyAction = "auto_forward"
	PolicyAsk         PolicyAction = "ask"
	PolicyIgnore      PolicyAction = "ignore"
)

// PortRange is a closed port interval; From == To is a single port.
type PortRange struct {
	From, To uint16
}

// PolicyCondition is one clause of a Forwarding Policy. Conditions within
// one policy are combined with AND: every set condition must match for the
// policy to match. A nil field means that dimension is unconstrained.
// Missing evidence never matches: an executable, ancestor, or working
// directory condition fails when the observation carries no process chain.
type PolicyCondition struct {
	RemotePorts          *PortRange
	BindScope            *ListenerBindScope
	Executable           *string // basename or full path; Linux case-sensitive
	AncestorExecutable   *string // basename or full path of any ancestor
	WorkingDirectoryTree *string // directory tree; path-component aware
}

// ForwardingPolicy is a saved, prioritized rule. Policies are evaluated by
// explicit priority, highest first; the first policy whose conditions all
// match decides. No matching policy defaults to Ask.
type ForwardingPolicy struct {
	ID         string
	Priority   int
	Action     PolicyAction
	Conditions []PolicyCondition
}

// PolicyVerdict is the outcome of evaluating policies against one Listener
// Observation. PolicyID is empty when no policy matched (the default Ask).
type PolicyVerdict struct {
	Action   PolicyAction
	PolicyID string
}

// evaluatePolicies applies the policy set to one observation. When a
// Listener has multiple attributable Listener Processes (one per Process
// Chain's direct holder), an automatic action requires every attributable
// process to produce a consistent result; otherwise the verdict is Ask for
// that policy, which still stops evaluation (first match owns the
// listener). Ports and bind scope are chain-independent and match with or
// without process evidence; executable, ancestor, and working-directory
// conditions need their evidence.
func evaluatePolicies(policies []ForwardingPolicy, observation ListenerObservation) PolicyVerdict {
	ordered := slices.Clone(policies)
	slices.SortStableFunc(ordered, func(left, right ForwardingPolicy) int {
		return right.Priority - left.Priority
	})
	for _, policy := range ordered {
		if verdict, matched := evaluatePolicy(policy, observation); matched {
			return verdict
		}
	}
	return PolicyVerdict{Action: PolicyAsk}
}

func evaluatePolicy(policy ForwardingPolicy, observation ListenerObservation) (PolicyVerdict, bool) {
	if len(observation.Processes) == 0 {
		// No process evidence: only chain-independent conditions can match.
		return evaluatePolicyForProcess(policy, observation, ProcessChain{})
	}
	var consistent *PolicyAction
	for _, chain := range observation.Processes {
		verdict, matched := evaluatePolicyForProcess(policy, observation, chain)
		if !matched {
			return PolicyVerdict{}, false
		}
		if consistent == nil {
			action := verdict.Action
			consistent = &action
		} else if *consistent != verdict.Action {
			// Attributable processes disagree: this policy cannot act
			// automatically, and it owns the listener — the result is Ask.
			return PolicyVerdict{Action: PolicyAsk, PolicyID: policy.ID}, true
		}
	}
	return PolicyVerdict{Action: *consistent, PolicyID: policy.ID}, true
}

func evaluatePolicyForProcess(policy ForwardingPolicy, observation ListenerObservation, chain ProcessChain) (PolicyVerdict, bool) {
	for _, condition := range policy.Conditions {
		if !conditionMatches(condition, observation, chain) {
			return PolicyVerdict{}, false
		}
	}
	return PolicyVerdict{Action: policy.Action, PolicyID: policy.ID}, true
}

func conditionMatches(condition PolicyCondition, observation ListenerObservation, chain ProcessChain) bool {
	if condition.RemotePorts != nil &&
		(observation.RemotePort < condition.RemotePorts.From || observation.RemotePort > condition.RemotePorts.To) {
		return false
	}
	if condition.BindScope != nil && observation.BindScope != *condition.BindScope {
		return false
	}
	if condition.Executable != nil {
		holder, ok := chainHolder(chain)
		if !ok || !executableMatches(*condition.Executable, holder.Executable) {
			return false
		}
	}
	if condition.AncestorExecutable != nil && !ancestorExecutableMatches(*condition.AncestorExecutable, chain) {
		return false
	}
	if condition.WorkingDirectoryTree != nil {
		holder, ok := chainHolder(chain)
		if !ok || !workingDirectoryMatches(*condition.WorkingDirectoryTree, holder.WorkingDirectory) {
			return false
		}
	}
	return true
}

// chainHolder is the direct socket holder: a Process Chain is ordered from
// the process that holds the socket up through its ancestors, so the holder
// is the chain's first entry.
func chainHolder(chain ProcessChain) (ProcessMetadata, bool) {
	if len(chain.Processes) == 0 {
		return ProcessMetadata{}, false
	}
	return chain.Processes[0], true
}

// executableMatches matches a policy pattern against a process executable.
// A pattern containing "/" is a full path and must match exactly; otherwise
// it is a basename. Linux matching is case-sensitive.
func executableMatches(pattern, executable string) bool {
	if strings.Contains(pattern, "/") {
		return executable == pattern
	}
	return path.Base(executable) == pattern
}

func ancestorExecutableMatches(pattern string, chain ProcessChain) bool {
	for _, process := range chain.Processes[1:] {
		if executableMatches(pattern, process.Executable) {
			return true
		}
	}
	return false
}

// workingDirectoryMatches is a path-component-aware tree check: the working
// directory equals the tree or sits under it. "/srv/app" does not contain
// "/srv/apple".
func workingDirectoryMatches(tree, cwd string) bool {
	tree = strings.TrimSuffix(tree, "/")
	return cwd == tree || strings.HasPrefix(cwd, tree+"/")
}
