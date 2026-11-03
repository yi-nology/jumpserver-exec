package koko

import (
	"strings"
	"testing"
)

// The asset panel's readiness signal.
//
// Measured 2026-10-25 on the live bastion: the panel paints the asset table
// and the row counter within about a second, and the status line keeps
// reading "正在加载资产…" — the "Ctrl+C" hint bar never appears at all, not
// even after 150 seconds of waiting.
//
// That is why the handshake used to fail on every asset with "koko TUI did
// not start: timeout 2m0s waiting for Ctrl+C", while the bastion itself was
// perfectly healthy. The counter is what is actually observable, so it is
// what the readiness check keys on.
func TestCounterTotal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"panel counter", "▶ 正在加载资产…            1-34/49", 49},
		{"single match", "1-1/1", 1},
		{"no counter", "just some output", 0},
		{"empty", "", 0},
		// The most recent paint wins: the panel repaints several times while
		// the list loads, and the last counter is the current one.
		{"latest wins", "1-34/49\nsome repaint\n1-35/49", 49},
	}
	for _, c := range cases {
		if got := counterTotal(c.in); got != c.want {
			t.Errorf("%s: counterTotal(%q) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}

// The counter must not be found in ordinary panel content, or the handshake
// would declare itself ready on a screen that has no asset list at all.
func TestCounterTotalIgnoresOrdinaryRows(t *testing.T) {
	screen := `31    node28            192.0.2.28     Linux    DEFAULT    bulk add node28 via API
32    MinIO-Console      198.51.100.253  Website  DEFAULT    MinIO 控制台 HTTP, 59001 端口
33    Nexus              192.0.2.193     Website  DEFAULT    Nexus 只有 8081 开放, 22/80 全 closed`
	if got := counterTotal(screen); got != 0 {
		t.Errorf("counterTotal found %d in ordinary rows, want 0", got)
	}
}

// The panel's counter is the signal that the search narrowed the list. Before
// the search the total is the whole inventory; after it, the number of
// matches. A filter that matches nothing is reported by koko itself.
func TestFilterNarrowsCounterTotal(t *testing.T) {
	before := "▶ 正在加载资产…            1-34/49"
	after := "▶ 正在加载资产…            1-1/1"
	bt, at := counterTotal(before), counterTotal(after)
	if !(bt > 0 && at > 0 && at < bt) {
		t.Errorf("expected the filter to narrow the total: before=%d after=%d", bt, at)
	}
	// A filter that does not narrow must not be mistaken for one that did.
	same := "1-34/49"
	if counterTotal(same) < bt {
		t.Errorf("unexpected narrowing")
	}
}

// The negative markers are what turn a stalled handshake into a specific
// error instead of a generic timeout.
func TestNegativeMarkersAreDistinct(t *testing.T) {
	if noAccount == noAsset {
		t.Error("the no-account and no-asset markers must differ")
	}
	for _, m := range []string{noAccount, noAsset} {
		if strings.TrimSpace(m) == "" {
			t.Error("marker must not be empty")
		}
	}
}
