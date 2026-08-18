package proxy

import "testing"

func TestAllocationPortsPrefersThenFallsBack(t *testing.T) {
	ports := allocationPorts(8080, false)
	if len(ports) != fallbackPortRoom+1 || ports[0] != 8080 || ports[len(ports)-1] != 8180 {
		t.Fatalf("allocationPorts(8080, false) = %v", ports)
	}
}

func TestAllocationPortsRequireSamePortIsExact(t *testing.T) {
	ports := allocationPorts(8080, true)
	if len(ports) != 1 || ports[0] != 8080 {
		t.Fatalf("allocationPorts(8080, true) = %v", ports)
	}
}

func TestAllocationPortsClampsAtMaxPort(t *testing.T) {
	ports := allocationPorts(65530, false)
	if ports[0] != 65530 || ports[len(ports)-1] != 65535 {
		t.Fatalf("allocationPorts(65530, false) last = %d, want 65535", ports[len(ports)-1])
	}
}
