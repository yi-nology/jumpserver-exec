package koko

import (
	"os"
	"strings"
	"testing"
)

// A CJK/wide character makes tmux advance the cursor by display width, not by
// byte count, so a painted row can contain padding that does not correspond to
// any character in the text. The parser must survive that.
func TestParseOutputWideChars(t *testing.T) {
	screen := "│1@" + testMarks.begin + "││2@hello││4@" + testMarks.rc + "0││\n" +
		"│3@中文世界││5@" + testMarks.end + "││"

	out, code, _, err := parseOutput(stripANSI(screen), testMarks, 0)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "hello\n中文世界" {
		t.Errorf("output = %q", out)
	}
}

// Output lines that themselves start with digits must not be mistaken for a
// `cat -n` number, because the separator is mandatory.
func TestParseOutputDigitLeadingPayload(t *testing.T) {
	screen := "│1@" + testMarks.begin + "││2@2024-01-01 ok││\n" +
		"│3@" + testMarks.rc + "0││\n" +
		"│4@" + testMarks.end + "││"

	out, _, _, err := parseOutput(stripANSI(screen), testMarks, 0)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if strings.TrimSpace(out) != "2024-01-01 ok" {
		t.Errorf("output = %q", out)
	}
}

// Spaces must survive the round trip. tmux eats them in the pane, so Exec
// substitutes 0x1E and the parser turns it back into a real space. A payload of
// "hello world" must not come back as "helloworld", which would silently corrupt
// column-oriented output such as `ls -la` or `df -h`.
func TestSpacesSurviveRoundTrip(t *testing.T) {
	sp := spaceMark
	// Padding sits where `cat -n` puts it: before the number. That padding is
	// also space-substituted, so it is the realistic on-screen shape.
	screen := "│1@" + testMarks.begin + "││2@" + sp + "hello" + sp + "world││\n" +
		"│3@" + sp + "total" + sp + "42" + sp + "files││\n" +
		"│4@" + testMarks.rc + "0││\n" +
		"│5@" + testMarks.end + "││"

	out, _, _, err := parseOutput(stripANSI(screen), testMarks, 0)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if strings.Contains(out, "\x1e") {
		t.Errorf("space marker leaked: %q", out)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("inner space lost: %q", out)
	}
	if !strings.Contains(out, "total 42 files") {
		t.Errorf("column spacing lost: %q", out)
	}
}

// Leading and trailing padding tmux adds to a column is not output; it must be
// stripped even though it now arrives as a restored space.
func TestColumnPaddingIsStripped(t *testing.T) {
	sp := spaceMark
	cols := cleanFrameColumns("││" + sp + sp + "2@" + sp + "node" + sp + sp + "││")
	if len(cols) != 1 {
		t.Fatalf("got %d columns %q", len(cols), cols)
	}
	if cols[0] != "2@ node" {
		t.Errorf("column = %q, want %q", cols[0], "2@ node")
	}

	// The parser is responsible for the space that follows the separator.
	out, _, _, err := parseOutput(stripANSI("│"+sp+"1@"+testMarks.begin+"││"+sp+"2@"+sp+"node││\n"+
		"│"+sp+"3@"+testMarks.rc+"0││\n│"+sp+"4@"+testMarks.end+"││"), testMarks, 0)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if strings.TrimSpace(out) != "node" {
		t.Errorf("output = %q, want %q", out, "node")
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
