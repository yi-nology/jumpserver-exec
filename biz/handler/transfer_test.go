package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/yi-nology/jumpserver-mcp-exec/internal/repository/koko"
)

// The upload side compares two checksums: the one computed on the bytes that
// were sent, and the one the host computed on the file it ended up with.
//
// The host's value comes from `sha256sum <file> | cut -c1-16`, so the local
// side has to produce exactly the same prefix -- a different length or a
// different encoding would make every transfer report a mismatch.
func TestSha256PrefixMatchesSha256sum(t *testing.T) {
	payload := []byte("hello from the inline content path\n")
	got := koko.Sha256Prefix(payload)

	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:])[:16]
	if got != want {
		t.Fatalf("sha256Prefix = %q, want %q", got, want)
	}
	if len(got) != 16 {
		t.Errorf("prefix length = %d, want 16 (it must match `cut -c1-16`)", len(got))
	}
}

// Cross-check against the real tool when it is available, because the contract
// is "the same string sha256sum prints", not "the same string we compute".
func TestSha256PrefixMatchesShellTool(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum not available")
	}
	payload := []byte("hello from the inline content path\n")
	f, err := os.CreateTemp("", "jmsx-sha-")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()

	out, err := exec.Command("sha256sum", f.Name()).Output()
	if err != nil {
		t.Fatalf("sha256sum: %v", err)
	}
	full := strings.Fields(string(out))[0]
	if got := koko.Sha256Prefix(payload); got != full[:16] {
		t.Errorf("sha256Prefix = %q, want %q (the first 16 hex chars of sha256sum)", got, full[:16])
	}
}

// A checksum that ignores the content would make every transfer look fine.
func TestSha256PrefixChangesWithContent(t *testing.T) {
	a := koko.Sha256Prefix([]byte("payload A"))
	b := koko.Sha256Prefix([]byte("payload B"))
	if a == b {
		t.Errorf("different payloads produced the same checksum %q", a)
	}
	if koko.Sha256Prefix([]byte("payload A")) != a {
		t.Error("the checksum is not stable for the same payload")
	}
}

// The CALL-level delivery of a sweep, and the question it answers: "can I
// run this whole sweep again?".
//
// The rule is the safe direction first: if ANY host may still be running
// its command, the sweep must never be replayed. Only when nothing ran
// anywhere AND every failure says "it never left" is the sweep safe to
// run again.
//
// A failure nobody classified is NOT "not sent": it might be a command that
// is running right now, and saying otherwise is exactly the mistake this
// whole column exists to prevent.
func TestTheCallLevelDeliveryOfASweep(t *testing.T) {
	cases := []struct {
		name         string
		maybeRunning bool
		okCount      int
		failCount    int
		allNotSent   bool
		want         string
	}{
		{"one host may still be running", true, 3, 1, false, "maybe_sent"},
		{"nothing ran and every failure says it never left", false, 0, 4, true, "not_sent"},
		{"some host ran, so the sweep as a whole did something", false, 2, 1, true, ""},
		{"a failure nobody classified", false, 0, 2, false, ""},
		{"nothing failed", false, 5, 0, true, ""},
	}
	for _, c := range cases {
		got := callDelivery(c.maybeRunning, c.okCount, c.failCount, c.allNotSent)
		if c.want == "" {
			if got != nil {
				t.Errorf("%s: the call says %q, want nothing said", c.name, *got)
			}
			continue
		}
		if got == nil {
			t.Errorf("%s: the call says nothing, want %q", c.name, c.want)
			continue
		}
		if *got != c.want {
			t.Errorf("%s: the call says %q, want %q", c.name, *got, c.want)
		}
	}
}
