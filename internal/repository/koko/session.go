package koko

// session.go keeps a koko connection open across several commands.
//
// Every jmsx_exec call used to walk the TUI from scratch: dial, wait for the
// pane, type the asset, confirm, reach a prompt. That is roughly 10 seconds
// before any work happens, so a look at five machines cost a minute, and
// transferring a file in chunks would have cost minutes.
//
// A Session pays that cost once. It is the same PTY, the same asset, so the
// audit trail is unchanged -- JumpServer still records one session with one
// user and one asset -- but the per-command cost drops to the time the command
// itself takes.
//
// Sessions are not safe for concurrent use; the tool layer serialises them.

import (
	"context"
	"fmt"
	"github.com/yi-nology/jumpserver-mcp-kit/model/jms"
	"strings"
	"sync"
	"time"

	"github.com/oekit/pkg/shellx"

	"github.com/yi-nology/jumpserver-mcp-exec/biz/model/jmsexec"
)

type Session struct {
	mu       sync.Mutex
	backend  Backend
	asset    string
	opened   time.Time
	commands int
	closed   bool

	// walk is how long opening the session took, measured when the session
	// was opened. It is the part of the session that the command itself
	// cannot shorten.
	walk time.Duration

	// commandTime is the total time this session's commands took.
	commandTime time.Duration
}

// OpenSession opens a session on an asset and parks it ready for commands.
//
// The backend is chosen by Open: the connection token when the bastion offers
// one, the koko TUI otherwise.
func OpenSession(ctx context.Context, cfg Config, asset string, timeout time.Duration) (*Session, error) {
	b, err := Open(ctx, cfg, asset, "", timeout)
	if err != nil {
		return nil, err
	}
	return &Session{backend: b, asset: asset, opened: time.Now(), walk: b.WalkDuration()}, nil
}

// Run executes one command on the session's asset.
func (s *Session) Run(command string, timeout time.Duration) (string, int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", -1, false, fmt.Errorf("session is closed")
	}
	started := time.Now()
	out, code, trunc, err := s.backend.Exec(command, timeout)
	elapsed := time.Since(started)
	s.commands++
	s.commandTime += elapsed
	recordCommand(elapsed)
	return out, code, trunc, err
}

// Exec returns the joined stdout of several commands, in order, without
// re-walking the TUI between them.
func (s *Session) Exec(commands []string, timeout time.Duration) ([]string, error) {
	results := make([]string, 0, len(commands))
	for i, c := range commands {
		per := timeout
		if per <= 0 {
			per = 60 * time.Second
		}
		out, _, _, err := s.Run(c, per)
		if err != nil {
			return results, fmt.Errorf("command %d/%d (%q): %w", i+1, len(commands), c, err)
		}
		results = append(results, out)
	}
	return results, nil
}

// Asset reports which machine this session is attached to.
func (s *Session) Asset() string { return s.asset }

// Stats describes how much the session has been used.
//
// The walk is the number that matters: it is what every command of this
// session did NOT have to pay again, and it is what a caller planning a
// fleet sweep has to budget for.
//
// The answer is the contract type, not a map: these six numbers are what a
// caller reads to decide whether a sweep is worth it, so they have to have a
// shape somebody can rely on — and there is exactly one definition of it.
func (s *Session) Stats() *jmsexec.SessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	walk := s.walk.Seconds()
	if walk <= 0 {
		// A session that opened in no measurable time is one that came
		// from the pool. The default is the pooled cost, because that is
		// what "no time" means here.
		walk = DefaultPooledSeconds
	}
	out := &jmsexec.SessionStats{
		Asset:       jms.Opt(s.asset),
		CommandsRun: jms.OptInt32(int32(s.commands)),
		OpenSeconds: jms.OptInt32(int32(time.Since(s.opened).Seconds())),
		WalkSeconds: jms.OptFloat64(round2(walk)),
		// Every command after the first avoided a walk of its own.
		ApproxSavedSec: jms.OptInt32(int32(float64(s.commands) * walk)),
	}
	if s.backend != nil {
		out.Backend = jms.Opt(s.backend.Name())
		out.Reused = jms.OptBool(s.backend.Reused())
	}
	if s.commands > 0 {
		out.CommandSeconds = jms.OptFloat64(round2(s.commandTime.Seconds() / float64(s.commands)))
	}
	return out
}

// Close drops the shell.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.backend.Close()
}

// UploadChunkBytes is how much one upload command may carry on this session.
func (s *Session) UploadChunkBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil {
		return uploadChunkBytes
	}
	return s.backend.UploadChunkBytes()
}

// CanCarryBytes says whether this session's backend can carry file bytes on
// its command channel.
func (s *Session) CanCarryBytes() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil {
		return false
	}
	return s.backend.CanCarryBytes()
}

// SizeOf reports the size of a file on the asset.
func (s *Session) SizeOf(remotePath string, timeout time.Duration) (int64, error) {
	out, _, _, err := s.Run(fmt.Sprintf("wc -c < %s | tr -d ' '", shellx.Quote(remotePath)), timeout)
	if err != nil {
		return 0, err
	}
	var size int64
	fmt.Sscanf(strings.TrimSpace(out), "%d", &size)
	return size, nil
}

// SHA256Prefix returns the host's checksum of a file.
//
// It is the host's own answer, not a recomputation here: comparing it with
// the digest of the bytes that arrived is what proves they are the same
// file's contents.
func (s *Session) SHA256Prefix(remotePath string, timeout time.Duration) (string, error) {
	out, _, _, err := s.Run(fmt.Sprintf("sha256sum %s | cut -c1-16", shellx.Quote(remotePath)), timeout)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// PullBytes reads a byte range of a file on the asset.
func (s *Session) PullBytes(remotePath string, start, total int64, timeout time.Duration) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil {
		return nil, fmt.Errorf("the session is closed")
	}
	return s.backend.PullBytes(remotePath, start, total, timeout)
}

// PutBytesAt writes bytes to a file on the asset, either replacing it or
// extending it.
//
// It exists because a chunked transfer needs both: the first chunk replaces
// the file and the rest extend it. Going through the session's lock keeps
// the two in step with the session's other use of the connection — the
// relay used to reach past it, which is the kind of shortcut that works
// until two things touch the connection at once.
func (s *Session) PutBytesAt(remotePath string, data []byte, extend bool, timeout time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil {
		return fmt.Errorf("the session is closed")
	}
	if extend {
		return s.backend.AppendBytes(remotePath, data, timeout)
	}
	return s.backend.PutBytes(remotePath, data, timeout)
}

// AppendBytes writes bytes at the END of a file on the asset.
//
// It is what a chunked transfer needs after its first chunk.
func (s *Session) AppendBytes(remotePath string, data []byte, timeout time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil {
		return fmt.Errorf("the session is closed")
	}
	return s.backend.AppendBytes(remotePath, data, timeout)
}

// Backend describes how this session was opened.
//
// It is one of the words the answer uses, so a caller can tell a session that
// walked the koko terminal from one that used a connection token.
func (s *Session) Backend() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil {
		return ""
	}
	return s.backend.Describe()
}

// Reused says whether this session came from the connection pool.
func (s *Session) Reused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil {
		return false
	}
	return s.backend.Reused()
}

// FallbackReason is why the session was opened through the slow path.
func (s *Session) FallbackReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil {
		return ""
	}
	return s.backend.FallbackReason()
}

// Account is the login name the session runs as, when the backend knows it.
func (s *Session) Account() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil {
		return ""
	}
	return s.backend.Account()
}

// splitLines normalises a chunked transfer: drop empties and keep order.
func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
