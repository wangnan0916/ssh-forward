package core

import "testing"

var (
	loopbackScope = BindLoopback
	wildcardScope = BindWildcard
)

func policyPort(port uint16) *PortRange {
	return &PortRange{From: port, To: port}
}

func policyRange(from, to uint16) *PortRange {
	return &PortRange{From: from, To: to}
}

func strptr(value string) *string { return &value }

// listenerObservation builds one ListenerObservation with the given chains;
// a nil chains argument yields an observation with no process evidence.
func listenerObservation(port uint16, chains []ProcessChain) ListenerObservation {
	return ListenerObservation{
		Family:     FamilyIPv4,
		BindScope:  BindLoopback,
		RemotePort: port,
		Processes:  chains,
	}
}

func chain(processes ...ProcessMetadata) ProcessChain {
	return ProcessChain{Processes: processes}
}

func leaf(executable, cwd string) ProcessMetadata {
	return ProcessMetadata{PID: 1, Executable: executable, WorkingDirectory: cwd}
}

func TestPolicyNoPoliciesDefaultAsk(t *testing.T) {
	verdict := evaluatePolicies(nil, listenerObservation(8080, nil))
	if verdict.Action != PolicyAsk || verdict.PolicyID != "" {
		t.Fatalf("evaluatePolicies(nil) = %+v, want Ask with no policy", verdict)
	}
}

func TestPolicySinglePortMatches(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}}}
	verdict := evaluatePolicies(policies, listenerObservation(8080, nil))
	if verdict.Action != PolicyAutoForward || verdict.PolicyID != "p1" {
		t.Fatalf("verdict = %+v, want auto_forward by p1", verdict)
	}
}

func TestPolicyPortRange(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyRange(8000, 9000)}}}}
	if verdict := evaluatePolicies(policies, listenerObservation(8500, nil)); verdict.Action != PolicyAutoForward {
		t.Fatalf("in-range verdict = %+v, want auto_forward", verdict)
	}
	if verdict := evaluatePolicies(policies, listenerObservation(9001, nil)); verdict.Action != PolicyAsk {
		t.Fatalf("out-of-range verdict = %+v, want Ask", verdict)
	}
}

func TestPolicyBindScopeLoopbackOnly(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{BindScope: &loopbackScope}}}}
	wildcard := ListenerObservation{Family: FamilyIPv4, BindScope: BindWildcard, RemotePort: 8080}
	if verdict := evaluatePolicies(policies, wildcard); verdict.Action != PolicyAsk {
		t.Fatalf("wildcard verdict = %+v, want Ask (loopback-only policy)", verdict)
	}
	if verdict := evaluatePolicies(policies, listenerObservation(8080, nil)); verdict.Action != PolicyAutoForward {
		t.Fatalf("loopback verdict = %+v, want auto_forward", verdict)
	}
}

func TestPolicyWildcardRequiresExplicitPolicy(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{BindScope: &wildcardScope}}}}
	observation := ListenerObservation{Family: FamilyIPv4, BindScope: BindWildcard, RemotePort: 8080}
	if verdict := evaluatePolicies(policies, observation); verdict.Action != PolicyAutoForward {
		t.Fatalf("wildcard verdict = %+v, want auto_forward by p1", verdict)
	}
}

func TestPolicyExecutableBasename(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{Executable: strptr("node")}}}}
	observation := listenerObservation(8080, []ProcessChain{chain(leaf("/usr/local/bin/node", "/srv/app"))})
	if verdict := evaluatePolicies(policies, observation); verdict.Action != PolicyAutoForward {
		t.Fatalf("basename verdict = %+v, want auto_forward", verdict)
	}
}

func TestPolicyExecutableFullPath(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{Executable: strptr("/usr/local/bin/node")}}}}
	observation := listenerObservation(8080, []ProcessChain{chain(leaf("/usr/local/bin/node", "/srv/app"))})
	if verdict := evaluatePolicies(policies, observation); verdict.Action != PolicyAutoForward {
		t.Fatalf("full-path verdict = %+v, want auto_forward", verdict)
	}
	other := listenerObservation(8080, []ProcessChain{chain(leaf("/opt/bin/node", "/srv/app"))})
	if verdict := evaluatePolicies(policies, other); verdict.Action != PolicyAsk {
		t.Fatalf("different-path verdict = %+v, want Ask", verdict)
	}
}

func TestPolicyExecutableCaseSensitive(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{Executable: strptr("NODE")}}}}
	observation := listenerObservation(8080, []ProcessChain{chain(leaf("/usr/local/bin/node", "/srv/app"))})
	if verdict := evaluatePolicies(policies, observation); verdict.Action != PolicyAsk {
		t.Fatalf("case-mismatch verdict = %+v, want Ask", verdict)
	}
}

func TestPolicyWorkingDirectoryTree(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{WorkingDirectoryTree: strptr("/srv/app")}}}}
	inside := listenerObservation(8080, []ProcessChain{chain(leaf("/usr/bin/python3", "/srv/app/sub"))})
	if verdict := evaluatePolicies(policies, inside); verdict.Action != PolicyAutoForward {
		t.Fatalf("inside-tree verdict = %+v, want auto_forward", verdict)
	}
	outside := listenerObservation(8080, []ProcessChain{chain(leaf("/usr/bin/python3", "/srv/apple"))})
	if verdict := evaluatePolicies(policies, outside); verdict.Action != PolicyAsk {
		t.Fatalf("component-adjacent verdict = %+v, want Ask (path components)", verdict)
	}
}

func TestPolicyAncestorExecutable(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{AncestorExecutable: strptr("make")}}}}
	observation := listenerObservation(8080, []ProcessChain{chain(
		leaf("/usr/bin/node", "/srv/app"),
		ProcessMetadata{PID: 2, Executable: "/usr/bin/make", WorkingDirectory: "/srv/app"},
	)})
	if verdict := evaluatePolicies(policies, observation); verdict.Action != PolicyAutoForward {
		t.Fatalf("ancestor verdict = %+v, want auto_forward", verdict)
	}
	// The leaf itself is not an ancestor; a make leaf must not match.
	noAncestor := listenerObservation(8080, []ProcessChain{chain(leaf("/usr/bin/node", "/srv/app"))})
	if verdict := evaluatePolicies(policies, noAncestor); verdict.Action != PolicyAsk {
		t.Fatalf("no-ancestor verdict = %+v, want Ask", verdict)
	}
}

func TestPolicyMissingEvidenceNeverMatches(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{Executable: strptr("node")}}}}
	verdict := evaluatePolicies(policies, listenerObservation(8080, nil))
	if verdict.Action != PolicyAsk {
		t.Fatalf("no-evidence verdict = %+v, want Ask", verdict)
	}
}

func TestPolicyConditionsAnd(t *testing.T) {
	policies := []ForwardingPolicy{{
		ID:     "p1",
		Action: PolicyAutoForward,
		Conditions: []PolicyCondition{
			{RemotePorts: policyPort(8080)},
			{BindScope: &loopbackScope},
			{Executable: strptr("node")},
			{WorkingDirectoryTree: strptr("/srv/app")},
		},
	}}
	observation := listenerObservation(8080, []ProcessChain{chain(leaf("/usr/bin/node", "/srv/app"))})
	if verdict := evaluatePolicies(policies, observation); verdict.Action != PolicyAutoForward {
		t.Fatalf("all-conditions verdict = %+v, want auto_forward", verdict)
	}
	// One condition broken (the executable) breaks the AND.
	wrongExe := listenerObservation(8080, []ProcessChain{chain(leaf("/usr/bin/python3", "/srv/app"))})
	if verdict := evaluatePolicies(policies, wrongExe); verdict.Action != PolicyAsk {
		t.Fatalf("broken-AND verdict = %+v, want Ask", verdict)
	}
}

func TestPolicyPriorityOrder(t *testing.T) {
	policies := []ForwardingPolicy{
		{ID: "low", Priority: 1, Action: PolicyIgnore, Conditions: []PolicyCondition{{RemotePorts: policyRange(1, 65535)}}},
		{ID: "high", Priority: 10, Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
	}
	verdict := evaluatePolicies(policies, listenerObservation(8080, nil))
	if verdict.Action != PolicyAutoForward || verdict.PolicyID != "high" {
		t.Fatalf("priority verdict = %+v, want auto_forward by high", verdict)
	}
	// A port only the low-priority policy matches still honors it.
	other := listenerObservation(8081, nil)
	if verdict := evaluatePolicies(policies, other); verdict.Action != PolicyIgnore || verdict.PolicyID != "low" {
		t.Fatalf("low-only verdict = %+v, want ignore by low", verdict)
	}
}

func TestPolicyMultipleProcessesConsistent(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{Executable: strptr("node")}}}}
	observation := listenerObservation(8080, []ProcessChain{
		chain(leaf("/usr/bin/node", "/srv/a")),
		chain(leaf("/usr/bin/node", "/srv/b")),
	})
	if verdict := evaluatePolicies(policies, observation); verdict.Action != PolicyAutoForward {
		t.Fatalf("consistent verdict = %+v, want auto_forward", verdict)
	}
}

func TestPolicyMultipleProcessesInconsistentAsk(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{Executable: strptr("node")}}}}
	observation := listenerObservation(8080, []ProcessChain{
		chain(leaf("/usr/bin/node", "/srv/a")),
		chain(leaf("/usr/bin/python3", "/srv/b")),
	})
	if verdict := evaluatePolicies(policies, observation); verdict.Action != PolicyAsk {
		t.Fatalf("inconsistent verdict = %+v, want Ask", verdict)
	}
}

func TestPolicyIgnoreAction(t *testing.T) {
	policies := []ForwardingPolicy{{ID: "p1", Action: PolicyIgnore, Conditions: []PolicyCondition{{RemotePorts: policyPort(9000)}}}}
	verdict := evaluatePolicies(policies, listenerObservation(9000, nil))
	if verdict.Action != PolicyIgnore || verdict.PolicyID != "p1" {
		t.Fatalf("ignore verdict = %+v, want ignore by p1", verdict)
	}
}
