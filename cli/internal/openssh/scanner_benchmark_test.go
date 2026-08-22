package openssh

import (
	"strconv"
	"strings"
	"testing"
)

var benchmarkPorts []uint16

func BenchmarkScanPortFrame256(b *testing.B) {
	var input strings.Builder
	input.WriteString("PF1\tB\t1\n")
	for port := 10_000; port < 10_000+maxObservedPorts; port++ {
		input.WriteString("PF1\tP\t1\t")
		input.WriteString(strconv.Itoa(port))
		input.WriteByte('\n')
	}
	input.WriteString("PF1\tE\t1\n")
	frame := input.String()

	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		if err := scanPortFrames(strings.NewReader(frame), func(ports []uint16) {
			benchmarkPorts = ports
		}); err != nil {
			b.Fatal(err)
		}
	}
}
