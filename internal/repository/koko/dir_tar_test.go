package koko

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// The tar stream is the whole protocol of jmsx_put_dir: if its shape is
// wrong (paths not relative, entries missing, sizes wrong), the extract on
// the host silently produces a different tree than the one that was sent.
// So the walk is pinned here, end to end, against a real fixture tree.
func TestWriteDirTarStreamsTheTreeRelative(t *testing.T) {
	root := t.TempDir()
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

	var buf bytes.Buffer
	counts := &dirTarCounts{}
	if err := writeDirTar(&buf, root, counts); err != nil {
		t.Fatalf("writeDirTar: %v", err)
	}
	if counts.files != 3 {
		t.Errorf("files = %d, want 3 (regular files only; the symlink is not a file)", counts.files)
	}
	wantBytes := int64(len("listen :8080") + len("deep") + len("#!/bin/sh\n"))
	if counts.bytes != wantBytes {
		t.Errorf("bytes = %d, want %d", counts.bytes, wantBytes)
	}

	got := map[string]string{}   // regular file -> content
	var dirs, links []string
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if filepath.IsAbs(h.Name) || h.Name == ".." || len(h.Name) > 0 && h.Name[0] == '/' {
			t.Errorf("path %q escapes the root — entries must be relative", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeReg:
			body, _ := io.ReadAll(tr)
			got[h.Name] = string(body)
		case tar.TypeDir:
			dirs = append(dirs, h.Name)
		case tar.TypeSymlink:
			links = append(links, h.Name+" -> "+h.Linkname)
		}
	}
	if got["app.conf"] != "listen :8080" || got["sub/nested.conf"] != "deep" ||
		got["sub/bin/tool.sh"] != "#!/bin/sh\n" {
		t.Errorf("file entries wrong: %v", got)
	}
	if len(dirs) == 0 {
		t.Error("no directory entries — the tree's structure would not survive the extract")
	}
	found := false
	for _, l := range links {
		if l == "current.conf -> app.conf" {
			found = true
		}
	}
	if !found {
		t.Errorf("symlink entry missing or wrong: %v", links)
	}
}
