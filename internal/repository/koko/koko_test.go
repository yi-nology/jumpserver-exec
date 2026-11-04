package koko

import (
	"os"
	"strings"
	"testing"
)

func TestPromptRe(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"bare", "opsuser@node:~$ ", true},
		{"framed", "││opsuser@node:~ $││", true},
		{"double framed", "││││opsuser@node:~ $││││", true},
		{"root hash", "││root@nas:~ # ││", true},
		{"prompt mid line", "opsuser@node:~ $ hostname", true},
		{"no prompt", "uid=1000(opsuser)gid=1000(opsuser)", false},
		{"bare email", "mail to bob@example.com now", false},
		{"empty", "", false},
		{"word hash", "see item # 3 for details", false},
	}
	for _, c := range cases {
		if got := promptRe.MatchString(c.in); got != c.want {
			t.Errorf("%s: promptRe(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

func TestStripANSI(t *testing.T) {
	raw := "\x1b[?1000l\x1b]8;;\x1b\\hello\r\x1b[K"
	if got := stripANSI(raw); got != "hello" {
		t.Errorf("stripANSI = %q, want %q", got, "hello")
	}
}

// koko's tmux packs short lines side by side; columns must be separated and the
// frame dropped.
func TestCleanFrameColumns(t *testing.T) {
	cols := cleanFrameColumns("││   1 node││   2 opsuser││")
	if len(cols) != 2 {
		t.Fatalf("got %d columns (%q), want 2", len(cols), cols)
	}
	if !strings.Contains(cols[0], "node") || !strings.Contains(cols[1], "opsuser") {
		t.Errorf("columns = %q", cols)
	}
	for _, c := range cols {
		if strings.ContainsAny(c, "│┃║") {
			t.Errorf("frame char survived in column %q", c)
		}
	}
}

// Exec converts cat -n's tab into @ because tmux collapses tabs to zero width,
// so the separator on a real screen is a visible byte, not whitespace.
func TestNumberedLineUsesVisibleSeparator(t *testing.T) {
	for _, in := range []string{"   1@node", "1@node", "   12@lots of spaces", "  999@value"} {
		m := numberedLine.FindStringSubmatch(in)
		if m == nil {
			t.Errorf("numberedLine did not match %q", in)
			continue
		}
		// group 1 = epoch (optional), group 2 = index, group 3 = payload
		if m[3] == "" {
			t.Errorf("numberedLine(%q) swallowed the text: %q", in, m)
		}
	}
	// The glued form that tmux produces when the tab is dropped must NOT match.
	if numberedLine.MatchString("2node") {
		t.Error("numberedLine matched a line with no separator; the payload " +
			"would be reported with its line number still attached")
	}
}

// The real screen packs several short lines onto one row; the numbers must
// bring them back in order and the numbering must not leak into the output.
func TestParseOutputSideBySide(t *testing.T) {
	screen := "\x1b[H" +
		"││$ hostname; whoami││\r\n" +
		"││" + testMarks.begin + "││\r\n" +
		"││2@" + spaceMark + "node ││4@opsuser││\r\n" +
		"││3@linux-kernel││5@" + testMarks.rc + "0││\r\n" +
		"││6@" + testMarks.end + "││\r\n" +
		"││opsuser@node:~ $││\r\n"

	out, code, _, err := parseOutput(stripANSI(screen), testMarks, 0)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	want := "node\nlinux-kernel\nopsuser"
	if strings.TrimSpace(out) != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if strings.Contains(out, testMarks.rc) {
		t.Errorf("rc marker leaked into output: %q", out)
	}
}

func TestParseOutputExitCode(t *testing.T) {
	screen := "$ exit 42\n" + testMarks.begin + "\n" +
		"   1@" + testMarks.rc + "42\n   2@" + testMarks.end + "\n"
	_, code, _, err := parseOutput(stripANSI(screen), testMarks, 0)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if code != 42 {
		t.Errorf("exit code = %d, want 42", code)
	}
}

func TestParseOutputNoMarkers(t *testing.T) {
	if _, _, _, err := parseOutput("just some text", testMarks, 0); err == nil {
		t.Error("expected an error when markers are missing")
	}
}

// Verbatim shape observed on the real koko screen on 2026-10-14: the markers,
// the numbered rows and the trailing prompt all arrive on the same painted
// row, with several short lines packed side by side by tmux.
func TestParseOutputRealScreenShape(t *testing.T) {
	screen := "─── x · 全屏f ─╮" +
		"│1@" + testMarks.begin + "││2@node││4@" + testMarks.rc + "0││" +
		"│3@opsuser││5@" + testMarks.end + "││opsuser@node:~ $││" +
		"╰───────────────╯"

	out, code, _, err := parseOutput(stripANSI(screen), testMarks, 0)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "node\nopsuser" {
		t.Errorf("output = %q, want %q", out, "node\\nopsuser")
	}
}

// The begin marker is echoed with its own cat -n number, so the region must be
// cut from the marker text itself, not from the start of its numbered row.
func TestParseOutputMarkerEchoIsNumbered(t *testing.T) {
	screen := "│   1@" + testMarks.begin + "││\n" +
		"│   2@value-one││\n" +
		"│   3@" + testMarks.rc + "3││\n" +
		"│   4@" + testMarks.end + "││\n"

	out, code, _, err := parseOutput(stripANSI(screen), testMarks, 0)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if strings.TrimSpace(out) != "value-one" {
		t.Errorf("output = %q, want %q", out, "value-one")
	}
}

func TestDefaultKeyPath(t *testing.T) {
	if p := testDefaultKeyPath(); !strings.HasSuffix(p, "id_rsa") {
		t.Errorf("defaultKeyPath = %q", p)
	}
	if os.Getenv("HOME") == "" {
		t.Skip("no HOME")
	}
}

// testMarks is a fixed token so fixtures stay readable; newMarks() is only
// needed to keep successive commands apart on a live session.
var testMarks = marks{begin: "__JMSX_BEGIN_t__", end: "__JMSX_END_t__", rc: "__JMSX_RC_t__"}

// testDefaultKeyPath is the conventional private key location.
//
// It is spelled out here rather than imported: the conf package builds this
// for real, but conf imports this package, so a test inside it cannot import
// conf back.
func testDefaultKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "~/.ssh/id_rsa"
	}
	return home + "/.ssh/id_rsa"
}
