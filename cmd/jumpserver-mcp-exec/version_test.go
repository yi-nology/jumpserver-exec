package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The version the binary reports must be the version the repository is
// tagged at.
//
// ⚠️ Why this is pinned: `go install ...@v0.3.1` installs a binary that says
// "0.3.0" when the constant was forgotten ⇒ the user reads `--version` and
// concludes they installed the wrong release. It happened once (the constant
// was left at 0.3.0 while v0.3.1 was published) and nothing caught it: the
// version string was in no test and no guard.
//
// ⚠️ The check reads the **tags** rather than a constant, so it cannot drift
// the same way: a release without a matching tag fails here, and a tag
// without a matching constant fails too.
func TestTheVersionMatchesTheLatestTag(t *testing.T) {
	tags := releaseTags(t)
	if len(tags) == 0 {
		t.Skip("no release tags in this repository (a clone without tags cannot check this)")
	}
	latest := tags[0]
	if Version != latest {
		t.Errorf("the binary reports version %s, but the latest tag is %s: "+
			"a user who installed @%s would read --version and think they "+
			"installed the wrong release", Version, latest, latest)
	}
}

// releaseTags returns the release tags of this repository, newest first.
//
// ⚠️ It reads the tags from git rather than from a hard-coded list: the list
// would have to be edited by hand every release, and a forgotten edit is
// exactly the failure this test exists to catch.
func releaseTags(t *testing.T) []string {
	t.Helper()
	out, err := runGit(t, "tag", "--list", "v*", "--sort=-v:refname")
	if err != nil {
		t.Skipf("cannot read the tags of this repository: %v", err)
	}
	var tags []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Only plain semantic versions count: a pre-release tag (v0.3.1-rc1)
		// is not what a user gets from "@latest".
		if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(line) {
			continue
		}
		tags = append(tags, strings.TrimPrefix(line, "v"))
	}
	return tags
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
