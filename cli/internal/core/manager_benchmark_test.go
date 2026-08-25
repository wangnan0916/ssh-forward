package core

import (
	"context"
	"testing"
)

var (
	benchmarkStatus Status
	benchmarkPlan   reconciliationPlan
)

func BenchmarkManagerStatus(b *testing.B) {
	cases := []struct {
		name  string
		ports int
	}{
		{name: "empty"},
		{name: "32_ports", ports: 32},
		{name: "256_ports", ports: 256},
	}
	for _, test := range cases {
		subject := &manager{
			host:      "dev",
			discovery: DiscoveryStatus{State: DiscoveryActive},
			listeners: make(map[uint16]Listener, test.ports),
			states:    make(map[forwardKey]ForwardStatus, test.ports),
		}
		for index := range test.ports {
			port := uint16(10_000 + index)
			subject.listeners[port] = Listener{Port: port, App: "node", WorkingDirectory: "/workspace/app"}
			subject.states[forwardKey{direction: RemoteToLocal, servicePort: port}] = ForwardStatus{
				Direction: RemoteToLocal, RemotePort: port, LocalPort: port, State: ForwardActive,
			}
		}
		b.Run(test.name, func(b *testing.B) {
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				status, err := subject.Status(ctx)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkStatus = status
			}
		})
	}
}

func BenchmarkPlanReconciliation256(b *testing.B) {
	desiredForwards := make(map[forwardKey]desiredForward, 256)
	workers := make(map[forwardKey]workerSnapshot, 256)
	reservedLocalPorts := make(map[uint16]struct{}, 128)
	for index := range 256 {
		var desired desiredForward
		if index%2 == 0 {
			desired = desiredRememberedForward(RememberedForward{
				RemotePort:    uint16(10_000 + index),
				LocalPort:     uint16(20_000 + index),
				AllowFallback: true,
			})
		} else {
			desired = desiredPublishedForward(PublishedForward{
				LocalPort:  uint16(20_000 + index),
				RemotePort: uint16(30_000 + index),
			})
			reservedLocalPorts[desired.preferred.LocalPort] = struct{}{}
		}
		key := desired.key()
		desiredForwards[key] = desired
		workers[key] = workerSnapshot{
			desired: desired,
			status:  forwardStatus(desired, ForwardActive, "", desired.preferred),
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		benchmarkPlan = planReconciliation(desiredForwards, workers, reservedLocalPorts)
	}
}
