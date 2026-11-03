package koko

import (
	"testing"

	"github.com/oekit/pkg/shellx"
)

// The destination path is interpolated into shell command lines on the asset,
// so it must never be able to smuggle in a second command or split into two
// arguments.
//
// The escaping itself is oekit's job now (pkg/shellx), so this only pins that
// the downstream calls the upstream primitive instead of growing a local
// copy again; the escaping semantics are pinned upstream.
func TestRemotePathsGoThroughTheUpstreamQuoting(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/tmp/plain.txt", "'/tmp/plain.txt'"},
		{"/tmp/with space.txt", "'/tmp/with space.txt'"},
		{"/tmp/a;rm -rf /", `'/tmp/a;rm -rf /'`},
		{"", "''"},
	}
	for _, c := range cases {
		if got := shellx.Quote(c.in); got != c.want {
			t.Errorf("shellx.Quote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A relative destination is normalised by oekit's pathx, and anything that
// climbs out of its root is refused outright.
func TestSafeRemotePathRefusesTraversal(t *testing.T) {
	bad := []string{
		"../etc/passwd",
		"a/../../etc/passwd",
		"a/b/../../../root/.ssh/authorized_keys",
		"",
		"   ",
		"/tmp/ok\nrm -rf /",
		"/tmp/../etc/passwd",
		// A relative path that mentions ".." is refused even when the ".."
		// cancels out ("a/../b" would clean to "b"). The path may cross a
		// symlink on the managed host, and "b" is not necessarily where
		// "b" lands — so the safe answer is to refuse, not to guess. The
		// absolute branch below has always refused it too; this is the
		// relative branch catching up.
		"a/../b",
		"opt/../app/x.txt",
	}
	for _, p := range bad {
		if got, err := safeRemotePath(p); err == nil {
			t.Errorf("safeRemotePath(%q) = %q, want it refused", p, got)
		}
	}
}

// The ordinary cases have to keep working: an absolute path is the normal way
// to write to /etc or /opt, and a relative one is normal for a home
// directory.
func TestSafeRemotePathKeepsOrdinaryPaths(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/tmp/app/config.yaml", "/tmp/app/config.yaml"},
		{"/opt/app/bin/run.sh", "/opt/app/bin/run.sh"},
		{"opt/app/x.txt", "opt/app/x.txt"},
		{"./opt/app/x.txt", "opt/app/x.txt"},
		{"opt/./app/x.txt", "opt/app/x.txt"},
	}
	for _, c := range cases {
		got, err := safeRemotePath(c.in)
		if err != nil {
			t.Errorf("safeRemotePath(%q) refused: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("safeRemotePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
