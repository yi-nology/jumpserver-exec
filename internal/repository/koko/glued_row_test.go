package koko

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The screen of a REAL failure, captured on 2026-10-28.
//
// The payload row was glued to the status bar's text: tmux repaints the
// status bar and then writes the pane row, and both land on the same STREAM
// line, separated by escape sequences rather than a newline.
//
// The row matcher wanted a column that STARTS with the number, so it dropped
// the row — and the answer came back with the exit code and NO OUTPUT. One
// answer in sixteen, on a fresh session (where the pane still repaints the
// status bar around the first command).
//
// The fixture is the raw capture, byte for byte, with the per-command markers
// replaced by the test's fixed ones. Nothing else was touched: the escape
// sequences, the status bar and the row are exactly what the bastion painted.
func readCapturedScreen(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "tui-glued-row.txt"))
	if err != nil {
		t.Fatalf("read the captured screen: %v", err)
	}
	return string(data)
}

// The captured screen must parse: the payload row survives being glued to
// the status bar, and the exit code comes back with it.
func TestThePayloadRowSurvivesBeingGluedToTheStatusBar(t *testing.T) {
	screen := readCapturedScreen(t)

	out, code, _, err := parseOutput(screen, testMarks, 1000)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "node" {
		t.Errorf("output = %q, want %q", out, "node")
	}
}

// The settle signal has to see the same row the parser sees.
//
// It is the same scan (resultSignature calls scanNumbered), so the count
// must be one row and not zero: a count of zero is what let the capture be
// taken before the payload was there.
func TestTheSettleSignalSeesTheGluedRow(t *testing.T) {
	screen := readCapturedScreen(t)

	rows, code, _ := resultSignature(screen, testMarks, 1000)
	if rows != 1 {
		t.Errorf("the signature counted %d rows, want 1", rows)
	}
	if code != 0 {
		t.Errorf("the signature saw the exit code %d, want 0", code)
	}
}

// The row matcher must not be fooled by digits that are NOT a row.
//
// The status bar carries the asset's address ("ssh://opsuser@node-071"), so
// a matcher that simply looked for "digits-dash-digits@" anywhere in the
// column would read the address as a row number. The epoch check is what
// keeps that out: the payload's epoch is the one this command was given.
func TestTheAddressInTheStatusBarIsNotARow(t *testing.T) {
	col := "ctrl+b:…ssh://opsuser@node-071"
	if cands := numberedCandidates(col); len(cands) > 0 {
		t.Errorf("the status bar text was taken for a numbered row (at %d)", cands[0].start)
	}
}

// The OUTPUT itself can contain something that looks like a row.
//
// A colour-highlighted match (grep --color on "2024@foo") is a segment of
// its own, right after an escape sequence. A matcher that took the LAST
// matching segment would let that fragment take the row's place — and the
// answer would carry the fragment instead of the command's output, silently.
//
// The same fragment without colour has no escape in front of it, so it stays
// inside the row's text and is reported as part of the output ✓.
func TestAColouredFragmentCannotTakeTheRowsPlace(t *testing.T) {
	screen := "1000-1@" + testMarks.begin + "\n" +
		"1000-2@\x1b[31m2024@foo\x1b[0m\n" +
		"1000-3@" + testMarks.rc + "0\n" +
		"1000-4@" + testMarks.end + "\n"

	out, code, _, err := parseOutput(screen, testMarks, 1000)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "2024@foo" {
		t.Errorf("output = %q, want %q", out, "2024@foo")
	}
}

// The answer must not carry escape sequences.
//
// The row matcher works on the raw stream now, so the text it hands back is
// stripped — and a pattern that only knows about digits leaves "ESC[0 q"
// (the cursor style) in the text. That leak was hidden for a long time
// because a column starting with an escape never matched the row matcher at
// all: the row was dropped, and the escape never reached an answer.
func TestTheAnswerCarriesNoEscapeSequences(t *testing.T) {
	screen := "1000-1@" + testMarks.begin + "\n" +
		"1000-2@\x1b[0 qnode\x1b[0m\n" +
		"1000-3@" + testMarks.rc + "0\n" +
		"1000-4@" + testMarks.end + "\n"

	out, _, _, err := parseOutput(screen, testMarks, 1000)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("the answer carries an escape sequence: %q", out)
	}
	if strings.TrimSpace(out) != "node" {
		t.Errorf("output = %q, want %q", out, "node")
	}
}

// A row without an epoch is not one of ours.
//
// Every row this tool writes carries the epoch of the command that wrote
// it, so a row without one can only be a piece of the output that happens
// to look like a row. Letting it in would replace the real row with a
// fragment of the output — silently, and without the epoch check that is
// supposed to keep the scrollback of earlier commands out.
func TestARowWithoutAnEpochIsNotAccepted(t *testing.T) {
	screen := "1000-1@" + testMarks.begin + "\n" +
		"5@noepoch\n" +
		"1000-3@" + testMarks.rc + "0\n" +
		"1000-4@" + testMarks.end + "\n"

	out, _, _, err := parseOutput(screen, testMarks, 1000)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if strings.Contains(out, "noepoch") {
		t.Errorf("a row without an epoch was taken for one: %q", out)
	}
}
