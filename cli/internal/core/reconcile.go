package core

import (
	"maps"
	"slices"
)

type workerSnapshot struct {
	desired desiredForward
	status  ForwardStatus
}

type reconciliationPlan struct {
	keep  []desiredForward
	stop  []forwardKey
	wait  []desiredForward
	start []desiredForward
}

func planReconciliation(
	desiredForwards map[forwardKey]desiredForward,
	workers map[forwardKey]workerSnapshot,
	reservedLocalPorts map[uint16]struct{},
) reconciliationPlan {
	var plan reconciliationPlan
	stoppingLocalPorts := make(map[uint16]struct{})
	for _, key := range sortedForwardKeys(workers) {
		worker := workers[key]
		desired, found := desiredForwards[key]
		reservedLocalPort, usesReservedLocalPort := reservedWorkerPort(key, worker.status, reservedLocalPorts)
		if found && sameForwardBehavior(worker.desired, desired) && !usesReservedLocalPort {
			plan.keep = append(plan.keep, desired)
			continue
		}
		plan.stop = append(plan.stop, key)
		if usesReservedLocalPort {
			stoppingLocalPorts[reservedLocalPort] = struct{}{}
		}
	}
	for _, key := range sortedForwardKeys(desiredForwards) {
		if _, running := workers[key]; running {
			continue
		}
		desired := desiredForwards[key]
		_, waitingForLocalPort := stoppingLocalPorts[desired.preferred.LocalPort]
		if desired.preferred.Direction == LocalToRemote && waitingForLocalPort {
			plan.wait = append(plan.wait, desired)
			continue
		}
		plan.start = append(plan.start, desired)
	}
	return plan
}

func sameForwardBehavior(left, right desiredForward) bool {
	return left.preferred == right.preferred && left.allowFallback == right.allowFallback
}

func reservedWorkerPort(key forwardKey, status ForwardStatus, reservedLocalPorts map[uint16]struct{}) (uint16, bool) {
	if key.direction != RemoteToLocal ||
		(status.State != ForwardStarting && status.State != ForwardActive) {
		return 0, false
	}
	_, reserved := reservedLocalPorts[status.LocalPort]
	return status.LocalPort, reserved
}

func sortedForwardKeys[T any](forwards map[forwardKey]T) []forwardKey {
	return slices.SortedFunc(maps.Keys(forwards), compareForwardKeys)
}

func compareForwardKeys(left, right forwardKey) int {
	if left.direction != right.direction {
		if left.direction == RemoteToLocal {
			return -1
		}
		return 1
	}
	return int(left.servicePort) - int(right.servicePort)
}
