package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The version the binary reports must be the version it was built from.
//
// ⚠️ Why this is pinned: `go install ...@v0.3.1` installed a binary that said
// "0.3.0" when the constant was forgotten ⇒ the user reads `--version` and
// concludes they installed the wrong release. It happened once (the constant
// was left at 0.3.0 while v0.3.1 was published) and nothing caught it: the
// version string was in no test and no guard.
//
// ⚠️ The comparison is against **the tag this build comes from**, not against
// a hard-coded list and not against "the newest tag in the repository":
//   - against a hard-coded list ⇒ the list has to be edited by hand every
//     release, and a forgotten edit is exactly the failure this catches;
//   - against "the newest tag" ⇒ the commit that *prepares* the next release
//     (version bumped, tag not yet pushed) would fail its own CI, and the
//     guard would be turned off by the first person it annoyed.
//
// ⇒ So: on a tagged build it checks; on an untagged build it says so and
// stops. The tag-triggered CI is where the check has teeth — that is the
// build that becomes the release.
func TestTheVersionMatchesTheTagItWasBuiltFrom(t *testing.T) {
	tag := buildTag(t)
	if tag == "" {
		t.Skip("this build is not from a release tag: there is nothing to " +
			"compare the version against (the tag-triggered CI checks this)")
	}
	want := strings.TrimPrefix(tag, "v")
	if Version != want {
		t.Errorf("the binary reports version %s, but it was built from tag %s: "+
			"a user who installed @%s would read --version and think they "+
			"installed the wrong release", Version, tag, tag)
	}
}

// buildTag returns the release tag this build comes from, or "" when the
// build is not from a tag.
//
// ⚠️ Two sources, in this order: the environment the CI sets, and the git
// history. The environment comes first because CI may check out with a
// shallow history that does not contain the tag object.
func buildTag(t *testing.T) string {
	t.Helper()
	if v := strings.TrimSpace(os.Getenv("JMS_RELEASE_TAG")); v != "" {
		return v
	}
	out, err := runGit(t, "describe", "--tags", "--exact-match", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// runGit runs a git command in the repository root and returns its output.
func runGit(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	return string(out), err
}

// repoRoot returns the repository root: the directory that holds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for d := dir; d != "/"; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	t.Fatal("no go.mod above the working directory")
	return ""
}
