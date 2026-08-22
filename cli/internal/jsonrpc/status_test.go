package jsonrpc

import (
	"context"
	"net"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type fixedManager struct {
	status core.Status
}

func (m fixedManager) Status(context.Context) (core.Status, error) { return m.status, nil }
func (fixedManager) Close(context.Context) error                   { return nil }

func TestStatusRoundTrip(t *testing.T) {
	server, clientConn := net.Pipe()
	want := core.Status{
		Host: "dev", Discovery: core.DiscoveryStatus{State: core.DiscoveryActive},
		Listeners: []uint16{5173},
		Forwards:  []core.ForwardStatus{{Port: 5173, State: core.ForwardActive}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeConn(ctx, server, fixedManager{status: want}) }()
	client, err := DialConn(ctx, clientConn)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	got, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != want.Host || len(got.Forwards) != 1 || got.Forwards[0] != want.Forwards[0] {
		t.Fatalf("status = %#v, want %#v", got, want)
	}
}
