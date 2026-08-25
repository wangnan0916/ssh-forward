package core

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestPlanReconciliation(t *testing.T) {
	remembered := desiredRememberedForward(RememberedForward{
		RemotePort: 3000, LocalPort: 13000, AllowFallback: true,
	})
	changedRemembered := desiredRememberedForward(RememberedForward{
		RemotePort: 3000, LocalPort: 14000, AllowFallback: true,
	})
	published := desiredPublishedForward(PublishedForward{
		LocalPort: 9222, RemotePort: 19222,
	})
	activeRemembered := forwardStatus(remembered, ForwardActive, "", remembered.preferred)
	activePublished := forwardStatus(published, ForwardActive, "", published.preferred)
	fallbackPublished := desiredPublishedForward(PublishedForward{
		LocalPort: 13001, RemotePort: 19001,
	})
	activeFallback := forwardStatus(remembered, ForwardActive, "", ForwardTarget{
		Direction: RemoteToLocal, RemotePort: 3000, LocalPort: 13001,
	})

	tests := []struct {
		name     string
		desired  map[forwardKey]desiredForward
		workers  map[forwardKey]workerSnapshot
		reserved map[uint16]struct{}
		want     reconciliationPlan
	}{
		{
			name:    "keep equivalent workers",
			desired: desiredForwardMap(remembered, published),
			workers: workerSnapshotMap(
				workerSnapshot{desired: remembered, status: activeRemembered},
				workerSnapshot{desired: published, status: activePublished},
			),
			reserved: map[uint16]struct{}{published.preferred.LocalPort: {}},
			want:     reconciliationPlan{keep: []desiredForward{remembered, published}},
		},
		{
			name:    "start missing worker",
			desired: desiredForwardMap(published),
			want:    reconciliationPlan{start: []desiredForward{published}},
		},
		{
			name:    "stop changed worker before replacement",
			desired: desiredForwardMap(changedRemembered),
			workers: workerSnapshotMap(
				workerSnapshot{desired: remembered, status: activeRemembered},
			),
			want: reconciliationPlan{stop: []forwardKey{remembered.key()}},
		},
		{
			name: "stop obsolete worker",
			workers: workerSnapshotMap(
				workerSnapshot{desired: published, status: activePublished},
			),
			want: reconciliationPlan{stop: []forwardKey{published.key()}},
		},
		{
			name:    "wait for reserved active worker",
			desired: desiredForwardMap(remembered, fallbackPublished),
			workers: workerSnapshotMap(
				workerSnapshot{desired: remembered, status: activeFallback},
			),
			reserved: map[uint16]struct{}{fallbackPublished.preferred.LocalPort: {}},
			want: reconciliationPlan{
				stop: []forwardKey{remembered.key()},
				wait: []desiredForward{fallbackPublished},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := planReconciliation(test.desired, test.workers, test.reserved)
			if diff := reconciliationPlanDiff(test.want, got); diff != "" {
				t.Fatalf("plan mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func FuzzPlanReconciliationReservation(f *testing.F) {
	f.Add(uint16(3000), uint16(13000), uint16(13001), uint16(19001), uint8(0))
	f.Add(uint16(3000), uint16(13000), uint16(13001), uint16(19001), uint8(1))
	f.Add(uint16(3000), uint16(13000), uint16(13001), uint16(19001), uint8(2))
	f.Fuzz(func(
		t *testing.T,
		remotePort, preferredLocalPort, actualLocalPort, publishedRemotePort uint16,
		stateIndex uint8,
	) {
		if remotePort == 0 || preferredLocalPort == 0 ||
			actualLocalPort == 0 || publishedRemotePort == 0 {
			return
		}
		remembered := desiredRememberedForward(RememberedForward{
			RemotePort: remotePort, LocalPort: preferredLocalPort, AllowFallback: true,
		})
		published := desiredPublishedForward(PublishedForward{
			LocalPort: actualLocalPort, RemotePort: publishedRemotePort,
		})
		states := [...]ForwardState{ForwardStarting, ForwardActive, ForwardFailed}
		state := states[int(stateIndex)%len(states)]
		status := forwardStatus(remembered, state, "", ForwardTarget{
			Direction:  RemoteToLocal,
			RemotePort: remotePort,
			LocalPort:  actualLocalPort,
		})
		got := planReconciliation(
			desiredForwardMap(remembered, published),
			workerSnapshotMap(workerSnapshot{desired: remembered, status: status}),
			map[uint16]struct{}{actualLocalPort: {}},
		)
		var want reconciliationPlan
		if state == ForwardFailed {
			want.keep = []desiredForward{remembered}
			want.start = []desiredForward{published}
		} else {
			want.stop = []forwardKey{remembered.key()}
			want.wait = []desiredForward{published}
		}
		if diff := reconciliationPlanDiff(want, got); diff != "" {
			t.Fatalf("plan mismatch for state %s (-want +got):\n%s", state, diff)
		}
	})
}

func FuzzReconciliationSequence(f *testing.F) {
	f.Add([]byte{0, 1, 2, 4, 1, 0, 2, 1, 3, 7, 1, 2, 6, 0, 0})
	f.Add([]byte{4, 3, 0, 0, 3, 3, 2, 3, 4, 1, 3, 0, 6, 0, 0})
	f.Fuzz(func(t *testing.T, operations []byte) {
		if len(operations) > 768 {
			operations = operations[:768]
		}
		remembered := make(map[uint16]RememberedForward)
		published := make(map[uint16]PublishedForward)
		listeners := make(map[uint16]Listener)
		workers := make(map[forwardKey]workerSnapshot)
		stopping := make(map[forwardKey]struct{})
		workerStates := [...]ForwardState{ForwardStarting, ForwardActive, ForwardFailed}
		for index := 0; index+2 < len(operations); index += 3 {
			operation := operations[index] % 8
			servicePort := uint16(10_000) + uint16(operations[index+1])
			localPort := uint16(20_000) + uint16(operations[index+2])
			switch operation {
			case 0:
				remembered[servicePort] = RememberedForward{
					RemotePort: servicePort, LocalPort: localPort, AllowFallback: true,
				}
			case 1:
				delete(remembered, servicePort)
			case 2:
				published[localPort] = PublishedForward{
					LocalPort: localPort, RemotePort: servicePort,
				}
			case 3:
				delete(published, localPort)
			case 4:
				listeners[servicePort] = Listener{
					Port: servicePort, WorkingDirectory: "/workspace/app",
				}
			case 5:
				delete(listeners, servicePort)
			case 6:
				for key := range stopping {
					delete(workers, key)
					delete(stopping, key)
				}
			case 7:
				keys := sortedForwardKeys(workers)
				if len(keys) != 0 {
					key := keys[0]
					worker := workers[key]
					worker.status.State = workerStates[int(operations[index+2])%len(workerStates)]
					if key.direction == RemoteToLocal {
						worker.status.LocalPort = localPort
					}
					workers[key] = worker
				}
			}

			intent := normalizedForwardingIntent(ForwardingIntent{
				RememberedForwards:    rememberedForwardValues(remembered),
				PublishedForwards:     publishedForwardValues(published),
				WorkingDirectoryRules: []string{"/workspace/**"},
			})
			desired := buildDesiredForwards(
				intent.RememberedForwards,
				intent.PublishedForwards,
				listeners,
				intent.WorkingDirectoryRules,
			)
			plan := planReconciliation(desired, workers, reservedLocalPorts(intent.PublishedForwards))
			assertReconciliationPlan(t, plan, desired, workers)

			for _, forward := range plan.keep {
				key := forward.key()
				worker := workers[key]
				worker.desired = forward
				worker.status.Automatic = forward.automatic
				workers[key] = worker
			}
			for _, key := range plan.stop {
				stopping[key] = struct{}{}
			}
			for _, forward := range plan.start {
				key := forward.key()
				workers[key] = workerSnapshot{
					desired: forward,
					status:  forwardStatus(forward, ForwardActive, "", forward.preferred),
				}
				delete(stopping, key)
			}
		}
	})
}

func assertReconciliationPlan(
	t *testing.T,
	plan reconciliationPlan,
	desired map[forwardKey]desiredForward,
	workers map[forwardKey]workerSnapshot,
) {
	t.Helper()
	workerActions := make(map[forwardKey]string, len(workers))
	for _, forward := range plan.keep {
		key := forward.key()
		if previous := workerActions[key]; previous != "" {
			t.Fatalf("worker has duplicate %s and keep actions: %#v", previous, key)
		}
		if _, found := workers[key]; !found {
			t.Fatalf("keep action has no worker: %#v", key)
		}
		if wanted, found := desired[key]; !found || wanted != forward {
			t.Fatalf("keep action is not desired: %#v", forward)
		}
		workerActions[key] = "keep"
	}
	for _, key := range plan.stop {
		if _, found := workers[key]; !found {
			t.Fatalf("stop action has no worker: %#v", key)
		}
		if previous := workerActions[key]; previous != "" {
			t.Fatalf("worker has both %s and stop actions: %#v", previous, key)
		}
		workerActions[key] = "stop"
	}
	if len(workerActions) != len(workers) {
		t.Fatalf("worker actions = %d, workers = %d", len(workerActions), len(workers))
	}
	desiredActions := make(map[forwardKey]string)
	recordDesiredActions := func(action string, forwards []desiredForward) {
		for _, forward := range forwards {
			key := forward.key()
			if _, found := workers[key]; found {
				t.Fatalf("%s action already has a worker: %#v", action, key)
			}
			if wanted, found := desired[key]; !found || wanted != forward {
				t.Fatalf("%s action is not desired: %#v", action, forward)
			}
			if previous := desiredActions[key]; previous != "" {
				t.Fatalf("desired forward has both %s and %s actions: %#v", previous, action, key)
			}
			desiredActions[key] = action
		}
	}
	recordDesiredActions("start", plan.start)
	recordDesiredActions("wait", plan.wait)
	missingWorkers := 0
	for key := range desired {
		if _, found := workers[key]; !found {
			missingWorkers++
		}
	}
	if len(desiredActions) != missingWorkers {
		t.Fatalf("desired actions = %d, desired forwards without workers = %d", len(desiredActions), missingWorkers)
	}
	for _, forward := range plan.wait {
		if forward.preferred.Direction != LocalToRemote {
			t.Fatalf("non-published forward is waiting: %#v", forward)
		}
		blocked := slices.ContainsFunc(plan.stop, func(key forwardKey) bool {
			worker := workers[key]
			return key.direction == RemoteToLocal &&
				(worker.status.State == ForwardStarting || worker.status.State == ForwardActive) &&
				worker.status.LocalPort == forward.preferred.LocalPort
		})
		if !blocked {
			t.Fatalf("waiting forward has no conflicting stop: %#v", forward)
		}
	}
}

func rememberedForwardValues(forwards map[uint16]RememberedForward) []RememberedForward {
	values := make([]RememberedForward, 0, len(forwards))
	for _, forward := range forwards {
		values = append(values, forward)
	}
	return values
}

func publishedForwardValues(forwards map[uint16]PublishedForward) []PublishedForward {
	values := make([]PublishedForward, 0, len(forwards))
	for _, forward := range forwards {
		values = append(values, forward)
	}
	return values
}

func desiredForwardMap(forwards ...desiredForward) map[forwardKey]desiredForward {
	result := make(map[forwardKey]desiredForward, len(forwards))
	for _, forward := range forwards {
		result[forward.key()] = forward
	}
	return result
}

func workerSnapshotMap(snapshots ...workerSnapshot) map[forwardKey]workerSnapshot {
	result := make(map[forwardKey]workerSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		result[snapshot.desired.key()] = snapshot
	}
	return result
}

func reconciliationPlanDiff(want, got reconciliationPlan) string {
	return cmp.Diff(want, got, cmp.AllowUnexported(
		reconciliationPlan{}, desiredForward{}, forwardKey{},
	))
}
