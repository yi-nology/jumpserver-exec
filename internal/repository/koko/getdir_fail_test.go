package koko

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// streamDirBackend is a backend whose Exec answers the tar probe and the du
// probe from memory, and whose RunOutput writes whatever the test gave it —
// the shape GetDir meets on the connection-token path.
type streamDirBackend struct {
	fakeBackend
	// duAnswer is the `du -sk` output the probe sees.
	duAnswer string
	// stream is written into RunOutput's writer before its verdict runs.
	stream []byte
	// verdict is what RunOutput ends with.
	verdict error
	code    int
}

func (b *streamDirBackend) Exec(command string, timeout time.Duration) (string, int, bool, error) {
	if strings.Contains(command, "du -sk") {
		return b.duAnswer, 0, false, nil
	}
	return b.fakeBackend.Exec(command, timeout)
}

func (b *streamDirBackend) CanCarryBytes() bool { return true }

func (b *streamDirBackend) RunOutput(_ string, w io.Writer, _ time.Duration) (int, error) {
	if len(b.stream) > 0 {
		if _, err := w.Write(b.stream); err != nil {
			return b.code, err
		}
	}
	return b.code, b.verdict
}

// aTarStream builds a valid tar stream with one regular file in it.
func aTarStream(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0o644}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

// A failed download must not leave a half-tree that looks like an answer.
// When THIS call created the destination, the partially extracted files are
// removed and the error says so (issue #22).
func TestGetDirFailureRemovesTheTreeItCreated(t *testing.T) {
	root := filepath.Join("/tmp", "jmsx-getdir-fail")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	local := filepath.Join(root, "pulled")

	b := &streamDirBackend{
		duAnswer: "42\t/remote\n",
		stream:   aTarStream(t, "half.txt", "partial content"),
		verdict:  errors.New("the command timed out (it may still be running on the machine)"),
	}
	s := &Session{backend: b, asset: "node-071"}

	_, err := s.GetDir("/remote", local, 30*time.Second)
	if err == nil {
		t.Fatal("a failed stream reported success")
	}
	if _, statErr := os.Stat(local); !os.IsNotExist(statErr) {
		t.Errorf("the partially extracted tree survived the failure: %v", statErr)
	}
	if !strings.Contains(err.Error(), "partial extraction removed") {
		t.Errorf("the error does not say the half-tree was removed: %v", err)
	}
	// The root cause rides first and stays readable.
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("the error lost the root cause: %v", err)
	}
}

// When the destination ALREADY existed, a failure must not delete it: the
// extracted files and the pre-existing ones cannot be told apart. The error
// says the tree is partial instead — the honest word, not a destructive
// guess.
func TestGetDirFailureLeavesAPreExistingTreeButSaysItIsPartial(t *testing.T) {
	root := filepath.Join("/tmp", "jmsx-getdir-fail")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	local := filepath.Join(root, "pulled")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	preExisting := filepath.Join(local, "keep.txt")
	if err := os.WriteFile(preExisting, []byte("mine"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	b := &streamDirBackend{
		duAnswer: "42\t/remote\n",
		stream:   aTarStream(t, "half.txt", "partial content"),
		verdict:  errors.New("the stream broke mid-transfer"),
	}
	s := &Session{backend: b, asset: "node-071"}

	_, err := s.GetDir("/remote", local, 30*time.Second)
	if err == nil {
		t.Fatal("a failed stream reported success")
	}
	if _, statErr := os.Stat(preExisting); statErr != nil {
		t.Fatalf("a pre-existing file was deleted by the failure path: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(local, "half.txt")); statErr != nil {
		t.Errorf("the partially extracted file vanished without a word (the error must name it): %v", statErr)
	}
	if !strings.Contains(err.Error(), "PARTIALLY extracted") || !strings.Contains(err.Error(), "do not use it as complete") {
		t.Errorf("the error does not say the surviving tree is partial: %v", err)
	}
}

// A non-zero tar exit with a partial stream is the same verdict: the
// half-tree goes (or is named), not silently kept.
func TestGetDirNonZeroExitAlsoRollsBack(t *testing.T) {
	root := filepath.Join("/tmp", "jmsx-getdir-fail")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	local := filepath.Join(root, "pulled")

	b := &streamDirBackend{
		duAnswer: "42\t/remote\n",
		stream:   aTarStream(t, "half.txt", "partial content"),
		code:     2,
	}
	s := &Session{backend: b, asset: "node-071"}

	_, err := s.GetDir("/remote", local, 30*time.Second)
	if err == nil {
		t.Fatal("a non-zero tar exit reported success")
	}
	if _, statErr := os.Stat(local); !os.IsNotExist(statErr) {
		t.Errorf("the partially extracted tree survived: %v", statErr)
	}
}

// The du probe feeds the deadline: a big tree gets more than the base, a
// missing probe gets exactly the base.
func TestRemoteTreeKBScalesTheDeadline(t *testing.T) {
	probe := func(out string, err error) int64 {
		run := func(string, time.Duration) (string, error) { return out, err }
		return remoteTreeKB(run, "/remote", time.Second)
	}
	if got := probe("42\t/remote\n", nil); got != 42 {
		t.Errorf("du answer parsed as %d KiB, want 42", got)
	}
	if got := probe("", nil); got != 0 {
		t.Errorf("an empty probe answered %d, want 0 (scale nothing)", got)
	}
	if got := probe("", errors.New("du: command not found")); got != 0 {
		t.Errorf("a failed probe answered %d, want 0 (scale nothing)", got)
	}
	if got := probe("du: cannot access\n0\n", nil); got != 0 {
		t.Errorf("garbage parsed as %d KiB, want 0", got)
	}

	// The deadline the download runs under: base + size at the floor rate,
	// capped — the SAME formula PutDir applies to its locally-known size.
	if got := dirStreamTimeout(300*time.Second, 512*1024); got != 301*time.Second {
		t.Errorf("512KiB added one second, got %v", got)
	}
	if got := dirStreamTimeout(300*time.Second, 0); got != 300*time.Second {
		t.Errorf("an unprobeable tree kept the base deadline, got %v", got)
	}
	if got := dirStreamTimeout(300*time.Second, 1<<30); got != 4*300*time.Second {
		t.Errorf("a huge tree is capped at 4x base, got %v", got)
	}
}
