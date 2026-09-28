package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildDesiredForwardsCombinesPersistentAndDiscoveredIntent(t *testing.T) {
	remembered := RememberedForward{RemotePort: 3000, LocalPort: 13000, AllowFallback: true}
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
		nil,
	)
	want := desiredForwardMap(desiredRememberedForward(remembered), desiredAutomaticForward(5173), desiredPublishedForward(published))
	require.Equal(t, want, got)
}

func TestIgnoredAppSkipsAutomaticListeners(t *testing.T) {
	remembered := RememberedForward{RemotePort: 47657, LocalPort: 47657}
	got := buildDesiredForwards(
		[]RememberedForward{remembered},
		nil,
		map[uint16]Listener{
			47657: {Port: 47657, App: "hunk", WorkingDirectory: "/workspace/app"},
			47658: {Port: 47658, App: "hunk", WorkingDirectory: "/workspace/app"},
			15173: {Port: 15173, App: "node", WorkingDirectory: "/workspace/app"},
		},
		[]string{"/workspace/**"},
		[]string{"hunk"},
	)
	want := desiredForwardMap(desiredRememberedForward(remembered), desiredAutomaticForward(15173))
	require.Equal(t, want, got)
}
