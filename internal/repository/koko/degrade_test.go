package koko

import (
	"io"
	"testing"
	"time"
)

// fakeBackend is a backend that answers from memory.
//
// It exists so that the degradation can be tested without a bastion: the
// degradation is the one place where a command runs on a DIFFERENT path than
// the session was opened with, and it is exactly the kind of path that has
// to be right — it decides what the answer says about the command.
type fakeBackend struct {
	out       string
	code      int
	truncated bool
	account   string
	identity  string
	closed    bool
}

func (f *fakeBackend) Exec(command string, timeout time.Duration) (string, int, bool, error) {
	return f.out, f.code, f.truncated, nil
}
func (f *fakeBackend) PullCommandOutput(command string, timeout time.Duration) (string, error) {
	return f.out, nil
}
func (f *fakeBackend) UploadChunkBytes() int                           { return 1024 }
func (f *fakeBackend) PutBytes(string, []byte, time.Duration) error    { return nil }
func (f *fakeBackend) CanCarryBytes() bool                             { return false }
func (f *fakeBackend) AppendBytes(string, []byte, time.Duration) error { return nil }
func (f *fakeBackend) PullBytes(string, int64, int64, time.Duration) ([]byte, error) {
	return nil, nil
}
func (f *fakeBackend) RunOutput(string, io.Writer, time.Duration) (int, error) {
	return 0, nil
}

func (f *fakeBackend) RunInput(string, io.Reader, time.Duration) (int, error) {
	return 0, ErrChannelTransferUnsupported
}
func (f *fakeBackend) WalkDuration() time.Duration { return 0 }
func (f *fakeBackend) Describe() string {
	return "jumpserver koko (bastion), recorded"
}
func (f *fakeBackend) Name() string     { return "koko TUI" }
func (f *fakeBackend) Account() string  { return f.account }
func (f *fakeBackend) Identity() string { return f.identity }
func (f *fakeBackend) FallbackReason() string {
	return ""
}
func (f *fakeBackend) Reused() bool { return false }
func (f *fakeBackend) Close()       { f.closed = true }

// The degradation is the one place where the answer can lie: the session
// was opened with a connection token, but the command ran on the terminal.
//
// Three things have to be true of the answer:
//
//   - it reports what ACTUALLY ran (the path, the output, the exit code);
//   - it does NOT report the token path's account for a command the token
//     path did not run — the terminal path does not choose an account, so
//     the honest answer is "unknown";
//   - it says WHY the fast path stopped working, so the operator has
//     something to act on.
func TestDegradeReportsWhatActuallyRan(t *testing.T) {
	fake := &fakeBackend{out: "node", code: 0, identity: "ai"}
	tb := &tokenBackend{
		asset:    "node-071",
		account:  "opsuser",
		identity: "delegated-user",
		cfg: Config{
			Host: "10.0.0.1", Port: 2222,
			Memory: NewBackendMemory(time.Minute),
		},
		fallback: func(time.Duration) (Backend, error) { return fake, nil },
	}

	out, code, truncated, err := tb.degrade("hostname", 30*time.Second, ErrTokenUnavailable)
	if err != nil {
		t.Fatalf("degrade: %v", err)
	}
	if out != "node" || code != 0 {
		t.Errorf("the degradation answered %q/%d, want %q/0", out, code, "node")
	}
	if truncated {
		t.Error("the degradation reported a truncation the fake did not make")
	}
	if tb.Describe() != fake.Describe() {
		t.Errorf("the answer says the session was %q, want %q", tb.Describe(), fake.Describe())
	}
	if tb.Name() != fake.Name() {
		t.Errorf("the answer names the backend %q, want %q", tb.Name(), fake.Name())
	}
	if tb.FallbackReason() == "" {
		t.Error("the answer does not say why the fast path stopped working")
	}
	// The account is UNKNOWN: the terminal path lets koko pick it, so the
	// token path's account must not be reported for a command it did not
	// run.
	if tb.Account() != "" {
		t.Errorf("the answer reports the account %q; the terminal path does not choose one", tb.Account())
	}
	// The identity is known: it is who the bastion authorised the session
	// as on the path that actually ran.
	if tb.Identity() != fake.Identity() {
		t.Errorf("the answer reports the identity %q, want %q", tb.Identity(), fake.Identity())
	}
	if !fake.closed {
		t.Error("the fallback session was not closed")
	}
}

// The truncation flag has to survive the degradation: the terminal path
// CAN cut the output short at the pane's height, and an answer that says
// "complete" while the top of the output is missing is a silent data loss.
func TestDegradePassesTheTruncationThrough(t *testing.T) {
	fake := &fakeBackend{out: "first line\nsecond line", code: 0, truncated: true}
	tb := &tokenBackend{
		asset:    "node-071",
		cfg:      Config{Host: "10.0.0.1", Port: 2222, Memory: NewBackendMemory(time.Minute)},
		fallback: func(time.Duration) (Backend, error) { return fake, nil },
	}
	_, _, truncated, err := tb.degrade("uptime", 30*time.Second, ErrTokenUnavailable)
	if err != nil {
		t.Fatalf("degrade: %v", err)
	}
	if !truncated {
		t.Error("the truncation was dropped on the way through the degradation")
	}
}

// A degradation that cannot use the other path has to say so — and it has
// to say that the fast path stopped working, because that is the part the
// operator can act on.
func TestDegradeSaysSoWhenTheOtherPathFails(t *testing.T) {
	tb := &tokenBackend{
		asset: "node-071",
		cfg:   Config{Host: "10.0.0.1", Port: 2222, Memory: NewBackendMemory(time.Minute)},
		fallback: func(time.Duration) (Backend, error) {
			return nil, ErrNoAccountOnAsset
		},
	}
	_, _, _, err := tb.degrade("hostname", 30*time.Second, ErrTokenUnavailable)
	if err == nil {
		t.Fatal("the degradation reported success although the other path failed")
	}
	if !contains(err.Error(), "connection-token path stopped working") {
		t.Errorf("the error does not say the fast path stopped working: %v", err)
	}
}

// contains is a substring check, so the test does not pull in a package for
// one call.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
