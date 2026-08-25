package core

import "slices"

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
	var stoppingLocalPorts map[uint16]struct{}
	for _, key := range sortedForwardKeys(workers) {
		worker := workers[key]
		desired, found := desiredForwards[key]
		reservedLocalPort, usesReservedLocalPort := reservedWorkerPort(
			key, worker.status, reservedLocalPorts,
		)
		if found && sameForwardBehavior(worker.desired, desired) && !usesReservedLocalPort {
			if plan.keep == nil {
				plan.keep = make([]desiredForward, 0, len(workers))
			}
			plan.keep = append(plan.keep, desired)
			continue
		}
		if plan.stop == nil {
			plan.stop = make([]forwardKey, 0, len(workers))
		}
		plan.stop = append(plan.stop, key)
		if usesReservedLocalPort {
			if stoppingLocalPorts == nil {
				stoppingLocalPorts = make(map[uint16]struct{})
			}
			stoppingLocalPorts[reservedLocalPort] = struct{}{}
		}
	}
	missingKeys := make([]forwardKey, 0, max(0, len(desiredForwards)-len(workers)))
	for key := range desiredForwards {
		if _, running := workers[key]; !running {
			missingKeys = append(missingKeys, key)
		}
	}
	slices.SortFunc(missingKeys, compareForwardKeys)
	for _, key := range missingKeys {
		desired := desiredForwards[key]
		_, waitingForLocalPort := stoppingLocalPorts[desired.preferred.LocalPort]
		if desired.preferred.Direction == LocalToRemote && waitingForLocalPort {
			if plan.wait == nil {
				plan.wait = make([]desiredForward, 0, len(missingKeys))
			}
			plan.wait = append(plan.wait, desired)
			continue
		}
		if plan.start == nil {
			plan.start = make([]desiredForward, 0, len(missingKeys))
		}
		plan.start = append(plan.start, desired)
	}
	return plan
}

func sameForwardBehavior(left, right desiredForward) bool {
	return left.preferred == right.preferred && left.allowFallback == right.allowFallback
}

func reservedWorkerPort(
	key forwardKey,
	status ForwardStatus,
	reservedLocalPorts map[uint16]struct{},
) (uint16, bool) {
	if key.direction != RemoteToLocal ||
		(status.State != ForwardStarting && status.State != ForwardActive) {
		return 0, false
	}
	_, reserved := reservedLocalPorts[status.LocalPort]
	return status.LocalPort, reserved
}

func sortedForwardKeys[T any](forwards map[forwardKey]T) []forwardKey {
	keys := make([]forwardKey, 0, len(forwards))
	for key := range forwards {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, compareForwardKeys)
	return keys
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
