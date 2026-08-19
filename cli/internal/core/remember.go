package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// rememberedPolicyPriority is the priority written for a Remembered
// Auto-forward. Hand-edited policies may use any priority; this value
// only identifies the simple rules add/remove create.
const rememberedPolicyPriority = 10

var (
	// ErrEmptyDirectory is returned when remembering a blank directory.
	ErrEmptyDirectory = errors.New("directory is empty")
	// ErrHostDirectory is returned when the directory is not an absolute
	// Development Host path (it must start with /).
	ErrHostDirectory = errors.New("directory must be an absolute path on the Development Host")
)

// RememberPort appends a simple Auto-forward for one remote port. It is
// idempotent: an existing simple rule for that port is left unchanged.
func RememberPort(policies []ForwardingPolicy, port uint16) ([]ForwardingPolicy, bool) {
	if portAutoForwardIndex(policies, port) >= 0 {
		return policies, false
	}
	return append(slices.Clone(policies), ForwardingPolicy{
		ID:       fmt.Sprintf("port-%d", port),
		Priority: rememberedPolicyPriority,
		Action:   PolicyAutoForward,
		Conditions: []PolicyCondition{{
			RemotePorts: &PortRange{From: port, To: port},
		}},
	}), true
}

// ForgetPort drops the simple Auto-forward RememberPort wrote. Complex
// policies that happen to mention the port are left alone.
func ForgetPort(policies []ForwardingPolicy, port uint16) ([]ForwardingPolicy, bool) {
	index := portAutoForwardIndex(policies, port)
	if index < 0 {
		return policies, false
	}
	updated := slices.Clone(policies)
	return append(updated[:index], updated[index+1:]...), true
}

// RememberDirectory appends a simple Auto-forward for a Development Host
// working-directory tree. The returned string is the stored path.
func RememberDirectory(policies []ForwardingPolicy, dir string) ([]ForwardingPolicy, string, bool, error) {
	dir, err := normalizeHostDir(dir)
	if err != nil {
		return nil, "", false, err
	}
	if dirAutoForwardIndex(policies, dir) >= 0 {
		return policies, dir, false, nil
	}
	return append(slices.Clone(policies), ForwardingPolicy{
		ID:       "dir-" + dir,
		Priority: rememberedPolicyPriority,
		Action:   PolicyAutoForward,
		Conditions: []PolicyCondition{{
			WorkingDirectoryTree: &dir,
		}},
	}), dir, true, nil
}

// ForgetDirectory drops the simple Auto-forward RememberDirectory wrote.
func ForgetDirectory(policies []ForwardingPolicy, dir string) ([]ForwardingPolicy, string, bool, error) {
	dir, err := normalizeHostDir(dir)
	if err != nil {
		return nil, "", false, err
	}
	index := dirAutoForwardIndex(policies, dir)
	if index < 0 {
		return policies, dir, false, nil
	}
	updated := slices.Clone(policies)
	return append(updated[:index], updated[index+1:]...), dir, true, nil
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

// SimpleAutoForwardPorts lists remote ports that RememberPort wrote.
func SimpleAutoForwardPorts(policies []ForwardingPolicy) []uint16 {
	var ports []uint16
	for _, policy := range policies {
		port, dir, ok := DescribeSimpleAutoForward(policy)
		if !ok || dir != "" {
			continue
		}
		ports = append(ports, port)
	}
	slices.Sort(ports)
	return ports
}

func portAutoForwardIndex(policies []ForwardingPolicy, port uint16) int {
	for index, policy := range policies {
		got, dir, ok := DescribeSimpleAutoForward(policy)
		if ok && dir == "" && got == port {
			return index
		}
	}
	return -1
}

// DescribeSimpleAutoForward reports the port or directory a Remembered
// Auto-forward rule created. Hand-edited policies return ok=false.
func DescribeSimpleAutoForward(policy ForwardingPolicy) (port uint16, dir string, ok bool) {
	if !simpleAutoForward(policy) {
		return 0, "", false
	}
	condition := policy.Conditions[0]
	if ports := condition.RemotePorts; ports != nil {
		if ports.From != ports.To {
			return 0, "", false
		}
		return ports.From, "", true
	}
	if tree := condition.WorkingDirectoryTree; tree != nil {
		return 0, *tree, true
	}
	return 0, "", false
}

func dirAutoForwardIndex(policies []ForwardingPolicy, dir string) int {
	for index, policy := range policies {
		_, got, ok := DescribeSimpleAutoForward(policy)
		if ok && got == dir {
			return index
		}
	}
	return -1
}

func simpleAutoForward(policy ForwardingPolicy) bool {
	if policy.Action != PolicyAutoForward || len(policy.Conditions) != 1 {
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
