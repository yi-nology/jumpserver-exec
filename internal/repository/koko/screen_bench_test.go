package koko

// Benchmarks for the screen parser — the one CPU hotspot of the TUI path.
//
// A capture is hundreds of KB of interleaved repaint frames (a 60-line result
// measured 967 KB), and resultSignature re-scans the whole buffer on every
// settle poll (every 250 ms for ~1 s per command). These benchmarks pin the
// per-scan cost so a parser change has to answer for its speed.

import (
	"fmt"
	"strings"
	"testing"
)

// syntheticCapture builds a capture that resembles a real one: `repaints`
// full-pane repaints of a `paneRows`-row pane, each row carrying frame bars,
// a space-marked payload, and cursor-move escapes between rows.
func syntheticCapture(paneRows, repaints, epoch int) string {
	var b strings.Builder
	m := newMarks()
	line := func(n int) string {
		text := strings.ReplaceAll(fmt.Sprintf("output line %d payload abcdefghij ", n), " ", spaceMark)
		return fmt.Sprintf("\x1b[%d;1H\x1b[2K││ %d-%d@%s││", n%paneRows+1, epoch, n, text)
	}
	// Begin marker row.
	b.WriteString(fmt.Sprintf("\x1b[1;1H%s\n", fmt.Sprintf(markBeginFmt, "tok")))
	for r := 0; r < repaints; r++ {
		for n := 2; n < paneRows; n++ {
			b.WriteString(line(n))
			b.WriteString("\n")
		}
	}
	// The rc and end markers, as the last rows.
	b.WriteString(fmt.Sprintf("\x1b[40;1H%d-%d@%s0\n", epoch, paneRows, m.rc))
	b.WriteString(fmt.Sprintf("\x1b[41;1H%s\n", m.end))
	return b.String()
}

func BenchmarkScanNumbered(b *testing.B) {
	capture := syntheticCapture(42, 30, 1000) // ≈ 40 rows × 30 repaints
	b.ReportAllocs()
	b.SetBytes(int64(len(capture)))
	for i := 0; i < b.N; i++ {
		scanNumbered(capture, newMarks(), 1000)
	}
}

func BenchmarkResultSignature(b *testing.B) {
	capture := syntheticCapture(42, 30, 1000)
	b.ReportAllocs()
	b.SetBytes(int64(len(capture)))
	for i := 0; i < b.N; i++ {
		resultSignature(capture, newMarks(), 1000)
	}
}

func BenchmarkCleanFrameColumns(b *testing.B) {
	row := "││   2-" + strings.ReplaceAll("output line 2 payload abcdefghij", " ", spaceMark) + "││  3-x││"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		cleanFrameColumns(row)
	}
}
