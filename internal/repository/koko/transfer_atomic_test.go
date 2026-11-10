package koko

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

// stagingBackend records every command the session sends and answers the
// probes a base64 upload meets: the symlink probe with the real target, the
// size probe with the payload's length, and the checksum probe with the
// payload's digest. Everything else succeeds without output.
type stagingBackend struct {
	fakeBackend
	commands []string
	payload  []byte
	// symlink answers the readlink probe; empty means "not a symlink".
	symlink string
}

func (b *stagingBackend) Exec(command string, _ time.Duration) (string, int, bool, error) {
	b.commands = append(b.commands, command)
	switch {
	case strings.Contains(command, "readlink -f"):
		if b.symlink != "" {
			return b.symlink + "\n", 0, false, nil
		}
		// Not a symlink: readlink fails and the fallback echoes the path.
		echoed := strings.SplitN(command, "|| echo ", 2)[1]
		return strings.Trim(echoed, "' ") + "\n", 0, false, nil
	case strings.Contains(command, "wc -c"):
		return fmt.Sprintf("%d", len(b.payload)), 0, false, nil
	case strings.Contains(command, "sha256sum"):
		sum := sha256.Sum256(b.payload)
		return hex.EncodeToString(sum[:])[:16], 0, false, nil
	}
	return "", 0, false, nil
}

// The base64 path keeps the three promises the tool description makes
// (#26, measured 2026-11-10 against the old sequence):
//
//   - nothing deletes the destination — the old path opened with
//     `rm -f <tmp> <dest>`, so a transfer that failed halfway left a HOLE
//     where the file was, not the old file;
//   - the payload lands on a sibling temp name and is renamed into place
//     with `mv` — the old path decoded straight onto the destination;
//   - the destination is resolved first, so a symlinked path keeps its link
//     (the payload goes to the link's target, not over the link).
//
// The guard reads the EMITTED SEQUENCE, not the code: what the asset sees
// is the contract.
func TestBase64UploadRenamesIntoPlaceAndNeverDeletesTheDestination(t *testing.T) {
	payload := []byte("# a config the deploy is about to replace\nlisten: 9\n")
	b := &stagingBackend{payload: payload}
	s := &Session{backend: b, asset: "node-071"}

	if _, err := s.putBytesOverBase64(payload, "/etc/app/config.yaml", 5*time.Second); err != nil {
		t.Fatalf("putBytesOverBase64: %v", err)
	}

	var sawRename, sawDecodeToSibling bool
	for _, cmd := range b.commands {
		// 1. No command may delete the destination — not up front, not on
		//    any cleanup path. The only `rm -f`s allowed are the transfer's
		//    own artefacts (the /tmp staging file and the sibling temp).
		if strings.Contains(cmd, "rm -f") && strings.Contains(cmd, "'/etc/app/config.yaml'") {
			t.Errorf("the sequence deletes the destination: %q — the old file must stay untouched until the rename", cmd)
		}
		// 2. Exactly one rename into place, from a sibling of the target.
		if strings.HasPrefix(cmd, "mv -f ") {
			sawRename = true
			if !strings.Contains(cmd, ".jmsx-tmp-") || !strings.HasSuffix(cmd, "'/etc/app/config.yaml'") {
				t.Errorf("the rename is not sibling→target: %q", cmd)
			}
		}
		// 3. The decode must aim at the sibling temp name, not the target.
		if strings.HasPrefix(cmd, "base64 -d ") {
			sawDecodeToSibling = strings.Contains(cmd, ".jmsx-tmp-")
		}
	}
	if !sawRename {
		t.Error("the sequence contains no `mv` — the payload is written in place instead of renamed into place")
	}
	if !sawDecodeToSibling {
		t.Error("the decode does not aim at a sibling temp name of the target")
	}
}

// A symlinked destination resolves to its target first: the rename lands on
// the RELEASE file, and the link itself is never deleted or replaced
// (#26 — the old path turned `current -> release/v1.conf` into a plain file
// while the release kept its old content: reported success, nothing done).
func TestBase64UploadResolvesTheSymlinkBeforeWriting(t *testing.T) {
	payload := []byte("NEW-CONTENT\n")
	b := &stagingBackend{payload: payload, symlink: "/srv/app/releases/v1.conf"}
	s := &Session{backend: b, asset: "node-071"}

	res, err := s.putBytesOverBase64(payload, "/srv/app/current", 5*time.Second)
	if err != nil {
		t.Fatalf("putBytesOverBase64: %v", err)
	}

	for _, cmd := range b.commands {
		if strings.HasPrefix(cmd, "mv -f ") && strings.HasSuffix(cmd, "'/srv/app/current'") {
			t.Errorf("the rename lands on the LINK instead of its target: %q", cmd)
		}
		if strings.Contains(cmd, "rm -f") && strings.Contains(cmd, "'/srv/app/current'") {
			t.Errorf("the sequence deletes the LINK: %q", cmd)
		}
	}
	sawRenameOntoTarget := false
	for _, cmd := range b.commands {
		if strings.HasPrefix(cmd, "mv -f ") && strings.HasSuffix(cmd, "'/srv/app/releases/v1.conf'") {
			sawRenameOntoTarget = true
		}
	}
	if !sawRenameOntoTarget {
		t.Error("the rename never lands on the symlink's resolved target")
	}
	// The answer still names the path the CALLER asked for.
	if res.RemotePath != "/srv/app/current" {
		t.Errorf("RemotePath = %q, want the caller's path", res.RemotePath)
	}
}

// The payload itself survives the round trip the sequence describes: the
// chunks the backend received decode back to the bytes that were sent.
// (This pins the staging shape — `: > tmp` then appends — against a future
// edit that regenerates the staging file mid-transfer.)
func TestBase64UploadStagesTheWholePayloadBeforeDecoding(t *testing.T) {
	payload := []byte(strings.Repeat("x", 2000)) // several chunks at 512 raw bytes
	b := &stagingBackend{payload: payload}
	s := &Session{backend: b, asset: "node-071"}

	if _, err := s.putBytesOverBase64(payload, "/tmp/big.bin", 5*time.Second); err != nil {
		t.Fatalf("putBytesOverBase64: %v", err)
	}

	var chunks int
	var staged strings.Builder
	for _, cmd := range b.commands {
		if !strings.HasPrefix(cmd, "printf '%s' '") {
			continue
		}
		chunks++
		body := strings.TrimPrefix(cmd, "printf '%s' '")
		// base64 carries no quotes, so the chunk ends at the first one —
		// the quote that closes it before the `>>`.
		staged.WriteString(body[:strings.IndexByte(body, '\'')])
	}
	if chunks < 2 {
		t.Fatalf("expected several chunks, saw %d", chunks)
	}
	got, err := base64.StdEncoding.DecodeString(staged.String())
	if err != nil {
		t.Fatalf("the staged chunks are not valid base64: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("the staged payload does not decode back to what was sent (%d bytes vs %d)", len(got), len(payload))
	}
}
