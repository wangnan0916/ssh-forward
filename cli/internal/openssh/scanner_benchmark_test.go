package openssh

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

var benchmarkListeners []core.Listener

func BenchmarkScanListenerFrame256(b *testing.B) {
	var input strings.Builder
	input.WriteString("PF2\tB\t1\n")
	metadata := base64.StdEncoding.EncodeToString([]byte("node\x00/workspace/app"))
	for port := 10_000; port < 10_000+maxObservedPorts; port++ {
		input.WriteString("PF2\tP\t1\t")
		input.WriteString(strconv.Itoa(port))
		input.WriteByte('\t')
		input.WriteString(metadata)
		input.WriteByte('\n')
	}
	input.WriteString("PF2\tE\t1\n")
	frame := input.String()

	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		if err := scanListenerFrames(strings.NewReader(frame), func(listeners []core.Listener) {
			benchmarkListeners = listeners
		}); err != nil {
			b.Fatal(err)
		}
	}
}
