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
	cmd := bgCheckCommand("/tmp/jmsx-bg-1.log", false, 0)
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
	cmd := bgCheckCommand("/tmp/jmsx-bg-1.log", true, 0)
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
		{"finished cleanly", "FINISHED rc=0\n1234\n", "success", "0", false},
		{"finished with a non-zero code", "FINISHED rc=2\n0\n", "error", "2", false},
		{"finished and cleaned", "FINISHED rc=1\nJMSX_BG_CLEANED\nSIZE=0\n", "error", "1", true},
		{"cancelled", "CANCELLED at=1731840000\n1234\n", "cancelled", "", false},
		{"timed out", "TIMEOUT\n1234\n", "timeout", "", false},
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

// The start line now records three markers: the exit-code file (.rc), the pid
// file (.pid — setsid makes that pid the process-group leader, which is what
// jmsx_bg_cancel kills), and the start timestamp (.started — the timeout
// state's clock).
func TestBgStartCommandRecordsPidAndStarted(t *testing.T) {
	line := bgStartCommand("/tmp/x.log", "sleep 1")
	for _, want := range []string{"/tmp/x.log.pid", "/tmp/x.log.started", "echo $$ >", "setsid nohup"} {
		if !strings.Contains(line, want) {
			t.Fatalf("start line missing %q: %s", want, line)
		}
	}
}

// The five-state vocabulary (kael): cancelled wins over the exit-code file,
// FINISHED maps to success/error by the recorded exit code, TIMEOUT and
// RUNNING are their own states.
func TestBgCheckFiveStates(t *testing.T) {
	if st, _, _ := parseBgCheckOutput("CANCELLED at=1731840000"); st != "cancelled" {
		t.Fatalf("got %q", st)
	}
	if st, rc, _ := parseBgCheckOutput("FINISHED rc=0"); st != "success" || rc != "0" {
		t.Fatalf("got %q rc=%q", st, rc)
	}
	if st, rc, _ := parseBgCheckOutput("FINISHED rc=3"); st != "error" || rc != "3" {
		t.Fatalf("got %q rc=%q", st, rc)
	}
	if st, _, _ := parseBgCheckOutput("TIMEOUT"); st != "timeout" {
		t.Fatalf("got %q", st)
	}
	if st, _, _ := parseBgCheckOutput("RUNNING"); st != "running" {
		t.Fatalf("got %q", st)
	}
}

// The check line knows the .cancelled marker (judged FIRST, before the
// exit-code file) and, only when a TTL is configured, the TIMEOUT branch off
// the .started timestamp. ttl=0 must not emit the timeout branch at all.
func TestBgCheckCommandCancelledAndTtl(t *testing.T) {
	line := bgCheckCommand("/tmp/x.log", false, 3600)
	for _, want := range []string{".cancelled", "CANCELLED", ".started", "TIMEOUT", "FINISHED", "RUNNING"} {
		if !strings.Contains(line, want) {
			t.Fatalf("check line missing %q: %s", want, line)
		}
	}
	c := strings.Index(line, "CANCELLED")
	f := strings.Index(line, "FINISHED")
	if c < 0 || f < 0 || c > f {
		t.Fatalf("CANCELLED must be judged before FINISHED: %s", line)
	}
	if strings.Contains(bgCheckCommand("/tmp/x.log", false, 0), "TIMEOUT") {
		t.Fatal("ttl=0 must not emit the timeout branch")
	}
}

// The cancel line's branches, in priority order: an already-recorded exit
// code means the job finished long ago; no pid file means an old job that
// cannot be cancelled; otherwise SIGTERM the whole process group, give it 5s
// (25 × 0.2s), and record 143 + the .cancelled marker when it dies.
func TestBgCancelCommandLifecycle(t *testing.T) {
	line := bgCancelCommand("/tmp/x.log")
	for _, want := range []string{"kill -TERM -", "ALREADY", "NOMARK", "CANCELLED", "STILLRUNNING", "143"} {
		if !strings.Contains(line, want) {
			t.Fatalf("cancel line missing %q: %s", want, line)
		}
	}
}

// The parser reads what the cancel builder writes — the two ends of one
// shell line, tested together.
func TestParseBgCancelOutput(t *testing.T) {
	cases := map[string]string{
		"CANCELLED":    "cancelled",
		"ALREADY rc=0": "already_finished",
		"ALREADY rc=2": "already_finished",
		"NOMARK":       "no_marker",
		"STILLRUNNING": "still_running",
	}
	for out, want := range cases {
		if st, _ := parseBgCancelOutput(out); st != want {
			t.Fatalf("%q → %q, want %q", out, st, want)
		}
	}
	if _, rc := parseBgCancelOutput("ALREADY rc=7"); rc != "7" {
		t.Fatal("rc not parsed")
	}
}
