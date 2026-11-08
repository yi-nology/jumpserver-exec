package koko

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// prescanFixture builds the tree both the prescan and the stream walk: two
// nested regular files, a third deeper one, a symlink, a directory, a unix
// socket and a fifo — every branch writeDirTar's walk can take.
//
// The root is deliberately short (/tmp, not t.TempDir()): a unix socket's
// bind fails when the socket path exceeds the OS sun_path limit, and the
// default temp dir on macOS is longer than that.
func prescanFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join("/tmp", fmt.Sprintf("jmsx-prescan-%d", os.Getpid()))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("app.conf", "listen :8080")
	write("sub/nested.conf", "deep")
	write("sub/bin/tool.sh", "#!/bin/sh\n")
	if err := os.Symlink("app.conf", filepath.Join(root, "current.conf")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// A socket and a fifo: the two shapes a tar stream leaves behind. Both
	// are skipped, counted and named — never fatal. The listener is closed
	// via t.Cleanup, NOT a defer: Go unlinks the socket file when its
	// listener closes, and a defer here would fire when the FIXTURE returns
	// — before the test ever walked the tree.
	sock, err := net.Listen("unix", filepath.Join(root, "agent.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { sock.Close() })
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	return root
}

// The prescan and the stream must agree EXACTLY: a dry run that drifted
// from the real walk would rehearse an upload that never happens. So this
// test walks the same fixture through both and compares every count.
func TestPrescanDirMatchesWhatTheStreamCarries(t *testing.T) {
	root := prescanFixture(t)

	st, err := PrescanDir(root)
	if err != nil {
		t.Fatalf("PrescanDir: %v", err)
	}

	var buf countingBuffer
	counts := &dirTarCounts{}
	if err := writeDirTar(&buf, root, counts); err != nil {
		t.Fatalf("writeDirTar: %v", err)
	}

	if st.Files != counts.files {
		t.Errorf("files: prescan %d, stream %d", st.Files, counts.files)
	}
	if st.Bytes != counts.bytes {
		t.Errorf("bytes: prescan %d, stream %d", st.Bytes, counts.bytes)
	}
	if st.Skipped != counts.skipped {
		t.Errorf("skipped: prescan %d, stream %d", st.Skipped, counts.skipped)
	}
	// The numbers themselves: 3 regular files, their content bytes, and the
	// two non-travellers.
	if st.Files != 3 {
		t.Errorf("files = %d, want 3 (directories and symlinks are structure)", st.Files)
	}
	if st.Bytes != int64(len("listen :8080")+len("deep")+len("#!/bin/sh\n")) {
		t.Errorf("bytes = %d, want the content total", st.Bytes)
	}
	if st.Skipped != 2 {
		t.Errorf("skipped = %d, want 2 (socket + fifo)", st.Skipped)
	}
	if len(st.SkippedNames) != 2 {
		t.Errorf("skipped names = %v, want both", st.SkippedNames)
	}
}

// A path that is not a directory (or not there at all) is refused, not
// counted as an empty tree: the rehearsal must not answer about a tree the
// upload could not walk either.
func TestPrescanDirRefusesNonDirectories(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := PrescanDir(file); err == nil {
		t.Error("a file was accepted as a prescan root")
	}
	if _, err := PrescanDir(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing path was accepted as a prescan root")
	}
}

// countingBuffer is the stream's sink: writeDirTar only needs a Writer.
type countingBuffer struct {
	data []byte
}

func (b *countingBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

var _ io.Writer = (*countingBuffer)(nil)
