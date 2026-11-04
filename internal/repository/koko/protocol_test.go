package koko

// The KoKo protocol facts, pinned.
//
// The strings this package keys on are the protocol: koko says them, and
// everything here depends on hearing them. They have already moved once —
// measured 2026-10-25, the hint bar stopped saying "Ctrl+C退出" and started
// saying "q:退出" / "enter:连接" — and the cost of that move was every
// session waiting out its full timeout while the bastion was perfectly
// healthy.
//
// So they are asserted against real screen text here: a change in koko's
// wording that breaks the detection has to fail a test that shows what the
// screen looks like, not turn into a two-minute timeout in production.

import (
	"strings"
	"testing"
)

// The connection banner is how the client learns WHICH asset it landed on.
//
// The terminal path lets koko pick the account, and the asset that was
// actually selected is only visible in the banner it paints. Reading the
// wrong one out of it means the answer describes a different machine than
// the one the command ran on.
func TestTheConnectionBannerNamesTheAsset(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   string
	}{
		{
			name:   "the banner koko paints on connect",
			screen: "正在连接 opsuser(opsuser)@192.0.2.71",
			want:   "192.0.2.71",
		},
		{
			name:   "the banner inside a tmux frame",
			screen: "││正在连接 ai(ai)@node-071││",
			want:   "node-071",
		},
		{
			name:   "the ssh:// form in the status bar",
			screen: "ctrl+b:… ssh://opsuser@node-071",
			want:   "node-071",
		},
		{
			name:   "no banner at all",
			screen: "some other screen",
			want:   "",
		},
	}
	for _, c := range cases {
		if got := connectedAsset(c.screen); got != c.want {
			t.Errorf("%s: connectedAsset = %q, want %q", c.name, got, c.want)
		}
	}
}

// The negative markers are the three ways the walk can be told to stop, and
// they have to be told apart: one of them means "this asset has no account",
// another means "no such asset", and the third means the panel is still
// loading. Treating them as one thing turns a precise refusal into a
// timeout.
func TestTheNegativeMarkersSayDifferentThings(t *testing.T) {
	markers := []string{noAccount, noAsset, loadAcct}
	for i, a := range markers {
		for j, b := range markers {
			if i != j && a == b {
				t.Errorf("two different failures share the marker %q", a)
			}
		}
	}
	if !strings.Contains(noAccount, "账号") {
		t.Errorf("the no-account marker does not mention an account: %q", noAccount)
	}
	if !strings.Contains(noAsset, "资产") {
		t.Errorf("the no-asset marker does not mention an asset: %q", noAsset)
	}
}

// The panel's readiness signal is the counter, not the hint bar.
//
// Measured 2026-10-25 on the live bastion: the status line keeps reading
// "正在加载资产…" and the "Ctrl+C" hint never appears at all, not even after
// 150 seconds. Keying on the hint bar made every session wait out its full
// timeout on a perfectly healthy bastion.
func TestTheReadinessSignalIsTheCounter(t *testing.T) {
	screen := "▶ 正在加载资产…            1-34/49"
	if got := counterTotal(screen); got != 49 {
		t.Errorf("counterTotal = %d, want 49", got)
	}
	// And the hint bar that is NOT there must not be the signal either.
	if strings.Contains(screen, "Ctrl+C") {
		t.Errorf("the readiness screen unexpectedly carries the old hint bar: %q", screen)
	}
}
