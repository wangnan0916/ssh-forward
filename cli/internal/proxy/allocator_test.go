package proxy

import "testing"

func TestAllocationPortsPrefersThenFallsBack(t *testing.T) {
	ports := allocationPorts(8080)
	if len(ports) != fallbackPortRoom+1 || ports[0] != 8080 || ports[len(ports)-1] != 8180 {
		t.Fatalf("allocationPorts(8080) = %v", ports)
	}
}

func TestAllocationPortsClampsAtMaxPort(t *testing.T) {
	ports := allocationPorts(65530)
	if ports[0] != 65530 || ports[len(ports)-1] != 65535 {
		t.Fatalf("allocationPorts(65530) last = %d, want 65535", ports[len(ports)-1])
	}
}
