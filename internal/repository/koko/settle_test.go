package koko

import (
	"strings"
	"testing"
)

// A row can be painted with its NUMBER and no TEXT yet.
//
// tmux paints the pane in passes, and a wide payload can be caught between
// them: the row is there, numbered, and its content has not arrived. The
// first sighting of a row used to win outright, so that empty paint was
// kept forever -- and the command's output came back as "" even though the
// command had plainly run.
//
// This is the screen shape that caused it: row 2 first appears empty, then
// the same row number is repainted with its text.
func TestParseOutputKeepsTheCompletePaintOfARow(t *testing.T) {
	screen := "││" + testMarks.begin + "││\r\n" +
		"││2@││\r\n" + // the row, numbered but not yet painted
		"││3@" + testMarks.rc + "0││\r\n" +
		"││4@" + testMarks.end + "││\r\n" +
		"││2@disk-58G││\r\n" + // the same row, now with its text
		"││opsuser@node:~ $││\r\n"

	out, code, _, err := parseOutput(stripANSI(screen), testMarks, 0)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "disk-58G") {
		t.Errorf("output = %q, want it to carry the row's text: the empty first "+
			"paint of a row must not win over the complete one", out)
	}
}

// The settle signal has to notice that a row's TEXT grew, not just that
// the row count stayed the same.
//
// The count does not move while a row is being filled in, so a
// count-only signature reports "settled" on a half-painted screen -- which
// is how the parse ended up looking at an empty row.
func TestResultSignatureNoticesTheTextGrowing(t *testing.T) {
	partial := "││" + testMarks.begin + "││\r\n" +
		"││2@││\r\n" +
		"││3@" + testMarks.rc + "0││\r\n" +
		"││4@" + testMarks.end + "││\r\n"
	complete := partial +
		"││2@disk-58G││\r\n"

	rowsA, codeA, paintA := resultSignature(stripANSI(partial), testMarks, 0)
	rowsB, codeB, paintB := resultSignature(stripANSI(complete), testMarks, 0)

	if rowsA != rowsB {
		t.Fatalf("the row count changed between the two screens (%d vs %d): "+
			"the fixture is wrong", rowsA, rowsB)
	}
	if codeA != codeB {
		t.Fatalf("the exit code changed between the two screens (%d vs %d): "+
			"the fixture is wrong", codeA, codeB)
	}
	if paintA == paintB {
		t.Error("the signature did not notice that a row's text grew: the settle " +
			"loop would break on a half-painted screen")
	}
}
