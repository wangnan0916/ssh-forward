package core

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestBuildDesiredForwardsCombinesPersistentAndDiscoveredIntent(t *testing.T) {
	remembered := RememberedForward{
		RemotePort: 3000, LocalPort: 13000, AllowFallback: true,
	}
	published := PublishedForward{LocalPort: 9222, RemotePort: 19222}
	got := buildDesiredForwards(
		[]RememberedForward{remembered},
		[]PublishedForward{published},
		map[uint16]Listener{
			3000:  {Port: 3000, WorkingDirectory: "/workspace/remembered"},
			5173:  {Port: 5173, WorkingDirectory: "/workspace/automatic"},
			8080:  {Port: 8080, WorkingDirectory: "/other"},
			19222: {Port: 19222, WorkingDirectory: "/workspace/published"},
		},
		[]string{"/workspace/**"},
	)
	want := desiredForwardMap(
		desiredRememberedForward(remembered),
		desiredAutomaticForward(5173),
		desiredPublishedForward(published),
	)
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(desiredForward{}, forwardKey{})); diff != "" {
		t.Fatalf("desired forwards mismatch (-want +got):\n%s", diff)
	}
}
