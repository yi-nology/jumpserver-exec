package handler

import (
	"strings"
	"testing"
)

// The check's shell line is the whole protocol of bg_check, and it once
// carried a real bug: the ".rc" suffix was appended BOTH inside the Quote
// (Quote(path+".rc")) and outside it (the format string's "%s.rc"), so the
// check tested `<path>.rc.rc` — a file that never exists — and every check
// forever answered RUNNING. These tests pin the two ends of that line: the
// suffix appears exactly once, and the cleanup variant only ever removes
// files in the FINISHED branch, after the exit code has been read.
func TestBgCheckCommandChecksTheMarkerOnce(t *testing.T) {
	cmd := bgCheckCommand("/tmp/jmsx-bg-1.log", false)
	// The suffix appears exactly TWICE (the -f test and the cat): once more
	// would mean it was appended both inside the Quote and outside it.
	if strings.Count(cmd, ".rc") != 2 {
		t.Errorf("the .rc suffix appears %d times, want 2 (-f test + cat): %s",
			strings.Count(cmd, ".rc"), cmd)
	}
	if strings.Contains(cmd, ".rc'.rc") || strings.Contains(cmd, ".rc.rc") {
		t.Errorf("the marker path is doubled — the check would test <path>.rc.rc, which never exists: %s", cmd)
	}
	// No cleanup in the plain check: nothing removes anything.
	if strings.Contains(cmd, "rm ") {
		t.Errorf("the plain check removes files: %s", cmd)
	}
	if !strings.Contains(cmd, "wc -c") {
		t.Errorf("the plain check does not report the output size: %s", cmd)
	}
}

func TestBgCheckCommandCleanupRemovesOnlyWhenFinished(t *testing.T) {
	cmd := bgCheckCommand("/tmp/jmsx-bg-1.log", true)
	// The rm sits INSIDE the FINISHED branch: a RUNNING task's files are its
	// live output and must not be touched.
	fin := strings.Index(cmd, "FINISHED")
	rm := strings.Index(cmd, "rm -f")
	els := strings.Index(cmd, "else echo RUNNING")
	if fin < 0 || rm < 0 || els < 0 {
		t.Fatalf("the cleanup command lost its shape: %s", cmd)
	}
	if rm < fin || rm > els {
		t.Errorf("the rm is not inside the FINISHED branch: %s", cmd)
	}
	// The exit code is read BEFORE the removal: the marker is the source of
	// the exit code, and removing it first would report finished with no
	// exit code.
	if cat := strings.Index(cmd, "cat"); cat < 0 || cat > rm {
		t.Errorf("the exit code is not read before the removal: %s", cmd)
	}
	if !strings.Contains(cmd, "JMSX_BG_CLEANED") {
		t.Errorf("the removal is not marked: %s", cmd)
	}
	// Three suffix occurrences here: the -f test, the cat, and the rm.
	if strings.Count(cmd, ".rc") != 3 {
		t.Errorf("the .rc suffix appears %d times, want 3 (-f + cat + rm): %s",
			strings.Count(cmd, ".rc"), cmd)
	}
	if strings.Contains(cmd, ".rc.rc") || strings.Contains(cmd, ".rc'.rc") {
		t.Errorf("the marker path is doubled: %s", cmd)
	}
}

// The parser reads what the builder writes: the two ends of the same shell
// line are tested together, so a marker rename fails here instead of
// silently unmooring the answer from the command.
func TestParseBgCheckOutput(t *testing.T) {
	cases := []struct {
		name     string
		out      string
		state    string
		exitCode string
		cleaned  bool
	}{
		{"still running", "RUNNING\n1234\n", "running", "", false},
		{"finished", "FINISHED rc=0\n1234\n", "finished", "0", false},
		{"finished with a non-zero code", "FINISHED rc=2\n0\n", "finished", "2", false},
		{"finished and cleaned", "FINISHED rc=1\nJMSX_BG_CLEANED\nSIZE=0\n", "finished", "1", true},
		{"running with cleanup asked", "RUNNING\nSIZE=512\n", "running", "", false},
	}
	for _, tc := range cases {
		state, code, cleaned := parseBgCheckOutput(tc.out)
		if state != tc.state || code != tc.exitCode || cleaned != tc.cleaned {
			t.Errorf("%s: state=%q exitCode=%q cleaned=%v, want %q/%q/%v",
				tc.name, state, code, cleaned, tc.state, tc.exitCode, tc.cleaned)
		}
	}
}

// The still-there note names the output's size when the check could read
// one, and says "unknown size" rather than going silent when it could not.
func TestBgBytesHint(t *testing.T) {
	if got := bgBytesHint("FINISHED rc=0\n4096\n"); got != "4096 bytes so far" {
		t.Errorf("bgBytesHint = %q, want the size", got)
	}
	if got := bgBytesHint("FINISHED rc=0\n"); got != "unknown size" {
		t.Errorf("bgBytesHint with no size line = %q, want \"unknown size\"", got)
	}
}
