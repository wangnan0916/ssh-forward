package core

import (
	"context"
	"testing"
)

var benchmarkStatus Status

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
			states:    make(map[uint16]ForwardStatus, test.ports),
		}
		for index := range test.ports {
			port := uint16(10_000 + index)
			subject.listeners[port] = Listener{Port: port, App: "node", WorkingDirectory: "/workspace/app"}
			subject.states[port] = ForwardStatus{RemotePort: port, LocalPort: port, State: ForwardActive}
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
