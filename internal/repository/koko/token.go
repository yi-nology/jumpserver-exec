package koko

// token.go opens a session on an asset with a CONNECTION TOKEN.
//
// What a connection token is. The bastion can mint a one-shot credential that
// is bound to one asset and one account. koko accepts it as an SSH login: the
// username is "JMS-{token id}" and the password is the token's value. Logging
// in that way lands DIRECTLY on the asset's shell — no asset panel, no search
// box, no waiting for a prompt.
//
// That is worth having because the TUI walk costs 7 to 9 seconds and the token
// path costs well under a second:
//
//	token minted      239ms   (measured against this bastion)
//	ssh handshake     205ms
//	command, pooled  150-250ms
//
// and because the token path returns what the TUI path has to reconstruct:
// the exit code is the command's own, the output is not squeezed through a
// 40-row pane, and stdout and stderr arrive separately.
//
// The protocol facts this file depends on, all measured against the live
// bastion on 2026-10-28:
//
//   - the endpoint is POST /api/v1/authentication/connection-token/ and the
//     authentication header is "Authorization: Token <key>" — NOT Bearer.
//   - the body's `account` is the ACCOUNT'S UUID. A username or an alias is
//     rejected with perm_account_invalid, which is where the idea that "koko
//     refuses connection tokens" comes from: it refuses the wrong account
//     field, not the mechanism.
//   - the body is {asset, account, protocol: "ssh", connect_method:
//     "web_cli"}.
//   - the token is single-use (is_reusable: false) and expires after five
//     minutes, so it authenticates ONE connection. A connection may then open
//     as many exec channels as it likes.
//
// ⚠️ The same body works on JumpServer v4 — and that was MEASURED, not
// reasoned. On a live v4.10.20 (2026-10-31): minting with the account's
// UUID SUCCEEDS, while the account's NAME and "@USER" are both refused with
// perm_account_invalid. ⇒ v4 wants the UUID, exactly like v5.
//
// ⚠️ A third-party v4 client states the opposite ("the token API needs the
// account ALIAS"), and that is exactly why it was worth measuring: the alias
// rule applies to the DYNAMIC accounts (@USER / @INPUT), whose alias really
// is "@USER" — this code picks a real account by UUID, so it never goes near
// that case.
//
// ⚠️ AND the error message lies about the cause: perm_account_invalid says
// "Account not found", but on v4 it is ALSO what you get when the account
// exists and simply nobody was granted the asset. The same message means two
// different things, and the second one is this repository's own
// asset-permission rule — so it costs ten minutes of misdiagnosis to tell
// them apart. ⇒ The permission is not optional for a session, and its
// absence looks exactly like a wrong account field.
//
// The identity of the session is worth spelling out. A token minted with the
// API token carries that token's owner's authority — which on this deployment
// is an administrator, and therefore MORE than the `ai` account the sessions
// have always run as. A token minted through a DELEGATION credential carries
// the delegated user's authority instead. So the delegation path is tried
// first, and the doctor says which of the two is in force.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/shellx"
	"golang.org/x/crypto/ssh"

	"github.com/yi-nology/jumpserver-kit/model/jms"
)

// The connection-token protocol.
const (
	// tokenProtocol and tokenConnectMethod are the pair the bastion needs
	// for a shell session. The SFTP pair is different: an ssh/web_cli token's
	// SFTP subsystem lands on koko's virtual root, so a file transfer has to
	// ask for protocol "sftp" with connect_method "web_sftp".
	tokenProtocol      = "ssh"
	tokenConnectMethod = "web_cli"
)

// tokenBackend is a session on an asset, opened with a connection token.
//
// The connection belongs to the pool: closing this session hands the
// connection back rather than tearing it down, and that is what makes the
// next session on the same asset nearly free.
type tokenBackend struct {
	conn *pooledConn
	pool *Pool

	// cfg is how a session is opened, kept so that the session can open
	// ANOTHER one on the other path when this one stops working.
	cfg Config

	// asset is the reference the caller named, and account is the login name
	// the session actually runs as. The account is resolved BEFORE the token
	// is minted, because the token binds one.
	asset   string
	account string

	// identity is who the token was minted for: the delegated user when a
	// delegation credential was used, the API token's owner otherwise. The
	// two carry different rights, so the answer says which one is in force.
	identity string

	// reused says the connection came from the pool rather than being
	// opened fresh. It is the difference between a session that paid a
	// handshake and one that did not.
	reused bool

	// want is the account the caller asked for, which may be empty. It is
	// kept because the pool is keyed by it, and a reconnect has to ask for
	// the same one.
	want string

	// walk is what opening the session cost: the token, the handshake and
	// the asset lookup that had to come first.
	walk time.Duration

	// mu serialises the commands on this connection.
	//
	// A koko connection cannot run two commands at once. Measured on 2026-10-28:
	// three channels opened concurrently on one connection, and one of them
	// came back with no output at all. So the commands on a connection are
	// strictly serial, and different connections are free to run in parallel.
	mu sync.Mutex

	// lastOut is the output of the last command, kept so PullCommandOutput
	// can answer without running anything again.
	lastOut string
	lastCmd string

	closed bool

	// degraded says the session had to fall back to the other path after
	// it was opened: the connection-token scheme stopped working while
	// the session was open, and the command had NOT been delivered, so it
	// was run on the terminal path — for the FIRST time.
	//
	// It is what the answer's `via` is built from, so the answer says
	// which path actually ran the command.
	degraded *degradation

	// degradedToTUI says the token path is known to be down for this
	// session. The next command then does not try to reopen a token
	// connection it knows will fail — it goes straight to the other path.
	degradedToTUI bool

	// fallback opens a session on the other path. It is a FIELD so that
	// the degradation can be tested without a bastion, the same reason the
	// pool's dial is a field.
	fallback func(timeout time.Duration) (Backend, error)
}

// degradation is what a session says about the fallback it made.
type degradation struct {
	// reason is why the fast path stopped working.
	reason string
	// via is how the command actually ran.
	via string
	// name is the short form of the same fact.
	name string
	// account is the login the command ran as — empty when it is unknown,
	// which is the case on the terminal path (koko picks the account).
	account string
	// identity is who the session was authorised as on the bastion side.
	identity string
}

// openToken opens a session with a connection token.
func openToken(ctx context.Context, cfg Config, asset, account string, timeout time.Duration) (Backend, error) {
	if cfg.API == nil {
		return nil, fmt.Errorf("%w: the bastion's REST API is not configured "+
			"(JUMPSERVER_URL and JUMPSERVER_TOKEN)", ErrTokenUnavailable)
	}
	started := time.Now()

	var (
		conn   *pooledConn
		reused bool
		err    error
	)
	if cfg.Pool != nil {
		conn, reused, err = cfg.Pool.get(ctx, cfg, asset, account, timeout)
	} else {
		// No pool: the connection is opened for this session alone and
		// closed with it. Correct, and it costs a handshake every time.
		conn, err = openOwnConnection(ctx, cfg, asset, account, timeout)
	}
	if err != nil {
		return nil, err
	}
	// The two costs are recorded apart: a sweep opens a session per host
	// and every one of those is a cold open, while a repeat call on the
	// same machine is a pooled hit. Pricing a sweep with the pooled number
	// would be a lie in one direction and the cold number a lie in the
	// other.
	if reused {
		recordPooled(time.Since(started), tagToken)
	} else {
		recordWalk(time.Since(started), tagToken)
	}
	return &tokenBackend{
		conn:     conn,
		pool:     cfg.Pool,
		cfg:      cfg,
		asset:    asset,
		want:     account,
		account:  conn.account,
		identity: conn.identity,
		walk:     time.Since(started),
		reused:   reused,
		fallback: func(timeout time.Duration) (Backend, error) {
			return openTUI(context.Background(), cfg, asset, account, fallbackWalkBudget)
		},
	}, nil
}

// fallbackWalkBudget is how long the terminal walk may take when a session
// degrades to it.
//
// The walk measures 7.25 to 9.12 seconds, so this is generous — and it is
// a BOUND, not a scale with the command's timeout: a command with a ten
// minute timeout must not make the walk wait ten minutes, and the command's
// own budget is spent on the command, not on the way to it.
const fallbackWalkBudget = 60 * time.Second

func (t *tokenBackend) Exec(command string, timeout time.Duration) (string, int, bool, error) {
	out, code, err := t.runText(command, timeout)
	if err != nil && errors.Is(err, errNotDelivered) {
		// The connection is broken. A fresh one is opened and the command
		// is tried once more — safe precisely because it was never
		// delivered.
		//
		// Unless this session already knows the token path is down: then
		// reopening a token connection is a waste of a round trip to the
		// bastion, and the other path is where the command goes anyway.
		//
		// ⚠️ The other path is used UNCONDITIONALLY here. This used to
		// ask `canDegrade(err)` — and that asks whether the error says
		// the token SCHEME is unavailable, which this error (the command
		// was not delivered) never says. So the branch never degraded,
		// and the command failed instead of running on the terminal that
		// this session had already proved it can use.
		//
		// It is safe: the command was NOT delivered, so running it on
		// the other path is its FIRST delivery, not a replay.
		if t.tokenPathDown() {
			// The cause is the error itself, unchanged: it already says
			// what is known ("the command was not delivered: …"), and
			// anything this layer added on top of it would be a
			// conclusion it cannot draw — the connection may be fine
			// while the SERVER refused the channel or the command.
			out, code, truncated, derr := t.degrade(command, timeout, err)
			if derr != nil {
				return out, -1, false, derr
			}
			return out, code, truncated, nil
		}
		if rerr := t.reconnect(); rerr != nil {
			// The fast path is not coming back: the token scheme is
			// unavailable now. The command was NOT delivered (this is
			// the errNotDelivered branch), so running it on the other
			// path is its FIRST delivery, not a replay — the one thing
			// the "never replay a delivered command" rule allows.
			if canDegrade(rerr) {
				out, code, truncated, derr := t.degrade(command, timeout, rerr)
				if derr != nil {
					return out, -1, false, derr
				}
				return out, code, truncated, nil
			}
			return out, -1, false, fmt.Errorf("%w (reopening the connection failed too: %v)", err, rerr)
		}
		out, code, err = t.runText(command, timeout)
	}
	if err != nil {
		return out, -1, false, err
	}
	t.remember(command, out)
	return out, code, false, nil
}

// errNotDelivered marks a failure that happened BEFORE the command reached
// the machine.
//
// It is the difference between a failure that may be retried and one that
// may not: a command that was delivered may still be running on the
// machine, and running it again is how `rm -rf` happens twice.
//
// It carries that state as well (not_sent), so the trail can answer
// "did it get there?" without reading the sentence.
var errNotDelivered = &deliveryError{
	text:     "the command was not delivered",
	delivery: jms.Delivery_not_sent,
}

// errPossiblyDelivered marks the case the SSH protocol makes unavoidable:
// the request was written and the server's REPLY was lost.
//
// The server answers an exec request BEFORE it runs the command (RFC 4254),
// so a lost reply means the command may already be running on the machine.
// x/crypto/ssh marks exactly that case with io.EOF: in mux.SendRequest the
// write failure returns the write error, and the lost-reply case returns
// io.EOF and nothing else.
//
// A command in this state is NEVER replayed.
var errPossiblyDelivered = &deliveryError{
	text:     "the command may already be running on the machine",
	delivery: jms.Delivery_maybe_sent,
}

// ErrNoAccountOnAsset says the asset has no account a session could use.
//
// It is an ASSET problem, not a mechanism problem: the terminal path would
// refuse the same asset the same way, and walking the terminal to find that
// out costs ten seconds.
var ErrNoAccountOnAsset = errors.New("no usable account on the asset")

// runText runs a command and returns its output, with stderr merged into
// stdout the way the shell's `2>&1` does.
func (t *tokenBackend) runText(command string, timeout time.Duration) (string, int, error) {
	out := &mergedBuffer{}
	code, err := t.run(command, nil, out, out, timeout)
	return out.String(), code, err
}

// runBinary runs a command and returns its stdout alone.
//
// The stderr is kept apart because the stdout may be BINARY: a file's
// bytes and an error message in one buffer is a corrupted file.
func (t *tokenBackend) runBinary(command string, timeout time.Duration) ([]byte, int, error) {
	var stdout, stderr bytes.Buffer
	code, err := t.run(command, nil, &stdout, &stderr, timeout)
	return stdout.Bytes(), code, err
}

// run executes one command on the connection.
//
// It holds the connection's lock for the whole command. One connection
// carries one command at a time: measured on 2026-10-28, three channels
// opened concurrently on one connection produced one command with no
// output at all.
func (t *tokenBackend) run(command string, stdin io.Reader, stdout, stderr io.Writer, timeout time.Duration) (int, error) {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return -1, fmt.Errorf("the session on %s is closed", t.asset)
	}

	// The wait for the connection is bounded by the timeout, and it can be
	// given up on: one connection serves every session on an asset, so a
	// command that hangs would otherwise hold all the others hostage, and
	// their own timeouts would never even start.
	wait, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	release, err := t.conn.gate.Acquire(wait)
	if err != nil {
		return -1, fmt.Errorf("the connection to %s is busy: another command is still running on it, "+
			"and this one gave up after %s: %w", t.asset, timeout, err)
	}
	defer release()

	sess, err := t.conn.client.NewSession()
	if err != nil {
		return -1, fmt.Errorf("%w: open an exec channel on %s: %v", errNotDelivered, t.asset, err)
	}
	defer sess.Close()

	if stdout != nil {
		sess.Stdout = stdout
	}
	if stderr != nil {
		sess.Stderr = stderr
	}
	if stdin != nil {
		sess.Stdin = stdin
	}

	// Start, not Run: the difference matters. Start returns as soon as the
	// command has been handed to the channel, so an error here is about
	// the DELIVERY — and the two ways it can fail mean different things.
	if err := sess.Start(command); err != nil {
		if errors.Is(err, io.EOF) {
			// The request was written and the server's reply was lost.
			// The server answers BEFORE it runs the command, so the
			// command may already be running on the machine. It is
			// never replayed.
			return -1, fmt.Errorf("%w: the connection to %s dropped before the server "+
				"answered: %v", errPossiblyDelivered, t.asset, err)
		}
		// The request never left: the write itself failed. The command
		// did not reach the machine, so trying again is safe.
		return -1, fmt.Errorf("%w: send the command to %s: %v", errNotDelivered, t.asset, err)
	}

	// The wait runs in its own goroutine so that the timeout below can fire
	// while it is still blocked. It is a GoSafe one: a panic in it would
	// otherwise take the whole server down, and the only thing it does is
	// hand the exit status over.
	waited := make(chan error, 1)
	async.GoSafeLabeled("koko-exec-wait", func() {
		waited <- sess.Wait()
	})

	var timerCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timerCh = timer.C
	}

	select {
	case err := <-waited:
		// Wait is a barrier: it returns only after every copy of stdout and
		// stderr has finished, so the buffer is complete when it does.
		if err == nil {
			return 0, nil
		}
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			// A non-zero exit is not a failure of the session: it is the
			// command's own answer, and the caller asked for it.
			return exitErr.ExitStatus(), nil
		}
		var missing *ssh.ExitMissingError
		if errors.As(err, &missing) {
			// The command was sent; the exit status never came back. It
			// may still be running there — so the state travels with the
			// error, and the sentence says it.
			return -1, fmt.Errorf("%w: the connection dropped before %s reported an exit status: %w",
				errPossiblyDelivered, t.asset, missing)
		}
		// ⚠️ NOT classified, on purpose.
		//
		// What arrives here is one of two things, and the two cannot be
		// told apart from the error alone:
		//
		//   - the OUTPUT copy failed after the exit status arrived (the
		//     common one: x/crypto's Session.Wait returns the copy error
		//     only when the exit status came through) ⇒ the command
		//     FINISHED, and calling it "may still be running" would put
		//     a finished command into the collection that stops people
		//     from replaying a command;
		//   - the exit status never arrived in a form we recognise (only
		//     a malformed exit-signal payload does that) ⇒ the command
		//     may still be running, but saying so here would be a guess.
		//
		// "Nobody said" is the honest answer, and the text says which of
		// the two it was.
		return -1, fmt.Errorf("the command failed on %s: %w", t.asset, err)
	case <-timerCh:
		// Closing the session unblocks Wait, so this returns promptly even
		// though the remote command may still be running. The wait for
		// that is bounded too: a wedged transport must not hold the
		// connection's lock, or every other session on that machine waits
		// for it as well.
		//
		// 协程形状是有界的（GitHub #15）：上面那条 GoSafe 协程是**每命令
		// 至多一个**的 Wait，命令完成或本分支的 sess.Close() 都会解开它
		//（channel 关闭时 Wait 必返回）；最坏情形——transport 彻底卡死
		// Wait 还不回——也只拖到 wedgeGrace 为止，然后这条连接被标记
		// broken 淘汰，不会复用也不会累积。并发超时（jmsx_exec_multi
		// 默认 4 台同时撞界）最多是「进行中的命令数」个短命协程，不随
		// 失败次数爬升。TestConcurrentCommandTimeoutsLeaveNoGoroutinesBehind
		// 把这个形状钉成测试。
		sess.Close()
		select {
		case <-waited:
		case <-time.After(wedgeGrace):
			// The transport is wedged. The connection is not usable any
			// more, and the command may still be running on the asset.
			t.conn.broken.Store(true)
		}
		return -1, fmt.Errorf(
			"the command on %s did not finish within %s: %w", t.asset, timeout, ErrCommandTimeout)
	}
}

// reconnect drops the broken connection and opens a new one.
//
// It is called only when the command was NOT delivered, which is what makes
// running the command again safe.
func (t *tokenBackend) reconnect() error {
	if t.pool == nil {
		return fmt.Errorf("this session has no connection pool")
	}
	t.pool.evict(t.conn)
	conn, _, err := t.pool.get(context.Background(), t.pool.cfg, t.asset, t.want, 90*time.Second)
	if err != nil {
		return err
	}
	// The fields are written under the lock that Close and PullCommandOutput
	// read them under. A session is used by one caller at a time today, but
	// the lock is what makes that true rather than what assumes it.
	t.mu.Lock()
	defer t.mu.Unlock()
	t.conn = conn
	t.account = conn.account
	t.identity = conn.identity
	return nil
}

// remember keeps the last command's output, so PullCommandOutput can answer
// without running anything again.
//
// It takes the lock, because PullCommandOutput reads the same fields under
// it: two goroutines on one session (one writing, one reading) would
// otherwise race on a plain string.
func (t *tokenBackend) remember(command, out string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastOut = out
	t.lastCmd = command
}

// PullCommandOutput returns the complete output of the last command.
//
// The token path never truncates: the output came off the wire, not off a
// 40-row pane. So this is the same text, and there is nothing to re-read.
func (t *tokenBackend) PullCommandOutput(command string, _ time.Duration) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastCmd == "" {
		return "", fmt.Errorf("no output was captured for a re-read; run the command first")
	}
	if t.lastCmd != command {
		return "", fmt.Errorf("the captured output belongs to a different command")
	}
	return t.lastOut, nil
}

// WalkDuration is what opening this session cost.
func (t *tokenBackend) WalkDuration() time.Duration { return t.walk }

// wedgeGrace is how long the code waits for a timed-out command's session
// to actually close.
//
// Closing the session is supposed to unblock the wait; when it does not, the
// transport is wedged and waiting longer helps nobody. The connection is
// marked broken instead, so the next caller gets a fresh one.
const wedgeGrace = 2 * time.Second

// tokenUploadChunkBytes is how much one upload command carries over the wire.
//
// It is the SSH channel's window that bounds this, not a terminal: 32KB of
// base64 is about 24KB of file, and a file of any size then costs a handful
// of round trips instead of hundreds.
const tokenUploadChunkBytes = 32 * 1024

// UploadChunkBytes is how much one upload command may carry.
func (t *tokenBackend) UploadChunkBytes() int { return tokenUploadChunkBytes }

// PutBytes writes a file on the asset.
//
// The bytes go on the exec channel's stdin, so there is no command-line
// length limit and no base64 expansion: one round trip carries the whole
// file. Measured against this bastion on 2026-10-28, a 1MB file arrives in
// 1.7s this way and takes 25s as base64 chunks.
//
// The command is `cat > file`, which truncates the destination — the same
// thing the base64 path did when it decoded over the target.
func (t *tokenBackend) PutBytes(remotePath string, data []byte, timeout time.Duration) error {
	return t.writeBytes("writing", "cat >", remotePath, data, timeout)
}

// CanCarryBytes says whether this backend can carry file bytes on its
// command channel. It can: the bytes go on the channel's stdin.
func (t *tokenBackend) CanCarryBytes() bool { return true }

// RunInput runs a command with a byte stream on the channel's stdin — the
// streamed-upload shape (`tar -xf - -C dir` drinking the archive). It is the
// generic body writeBytes uses, offered on its own for commands whose stdin
// IS the payload.
func (t *tokenBackend) RunInput(command string, stdin io.Reader, timeout time.Duration) (int, error) {
	return t.runWithInput(command, stdin, timeout)
}

// RunOutput is RunInput's mirror: the command's stdout streams straight into
// the caller's writer (`tar -cf - -C dir` writing the archive onto it), so a
// directory download never holds the whole archive in memory. The stderr is
// discarded into a bounded buffer for the error message only.
func (t *tokenBackend) RunOutput(command string, stdout io.Writer, timeout time.Duration) (int, error) {
	var stderr bytes.Buffer
	return t.run(command, nil, stdout, &stderr, timeout)
}

// AppendBytes writes bytes at the END of a file on the asset.
//
// It is the same channel as PutBytes, with `cat >>` instead of `cat >`:
// the first chunk of a transfer replaces the file and the rest extend it.
func (t *tokenBackend) AppendBytes(remotePath string, data []byte, timeout time.Duration) error {
	return t.writeBytes("appending to", "cat >>", remotePath, data, timeout)
}

// writeBytes is the one body PutBytes and AppendBytes share: the command
// differs by one redirect and the error by one verb — everything else,
// including the not-delivered retry, is the same write.
//
// ⚠️ The retry below is the ONLY place a write may run twice: it fires when
// the command never reached the machine (errNotDelivered), which is the one
// failure a second run cannot double-apply.
func (t *tokenBackend) writeBytes(verb, redirect, remotePath string, data []byte, timeout time.Duration) error {
	cmd := fmt.Sprintf("%s %s", redirect, shellx.Quote(remotePath))
	code, err := t.runWithInput(cmd, bytes.NewReader(data), timeout)
	if err != nil && errors.Is(err, errNotDelivered) {
		// The command never reached the machine, so writing it again is
		// safe.
		if rerr := t.reconnect(); rerr != nil {
			return fmt.Errorf("%w (reopening the connection failed too: %v)", err, rerr)
		}
		code, err = t.runWithInput(cmd, bytes.NewReader(data), timeout)
	}
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s %s on %s exited %d", verb, remotePath, t.asset, code)
	}
	return nil
}

// runWithInput runs a command with data on the channel's stdin.
func (t *tokenBackend) runWithInput(command string, stdin io.Reader, timeout time.Duration) (int, error) {
	var stdout, stderr bytes.Buffer
	return t.run(command, stdin, &stdout, &stderr, timeout)
}

// PullBytes reads a byte range of a remote file.
//
// The token path has no pane, so a whole range comes back in ONE round trip:
// the bytes come off the channel's stdout, unchanged — no base64 to encode
// and no base64 to decode, and a binary file survives intact.
//
// The range is still chunked, because an answer that holds a whole
// gigabyte in memory is not an answer. A chunk that comes back short is
// reported rather than padded: the caller advances its offset by what it
// got, so an inflated answer would corrupt the stream.
func (t *tokenBackend) PullBytes(remotePath string, start, total int64, timeout time.Duration) ([]byte, error) {
	if total <= 0 {
		return nil, nil
	}
	var out []byte
	for off := start; off < start+total; {
		want := start + total - off
		if want > tokenChunkBytes {
			want = tokenChunkBytes
		}
		part, err := t.pullRange(remotePath, off, want, timeout)
		if err != nil {
			// The bytes that DID arrive come back WITH the error, so a
			// caller can decide what to do with them. Returning them
			// without the error would let a transfer report success on
			// half a file.
			return out, err
		}
		if len(part) == 0 {
			break
		}
		out = append(out, part...)
		off += int64(len(part))
	}
	return out, nil
}

// tokenChunkBytes is how many raw bytes one round trip carries.
//
// The TUI path is limited to about 2000 raw bytes per line because the pane
// is 220 columns wide; here the limit is the size of the answer, so the
// chunk is large enough that a megabyte is a handful of round trips.
const tokenChunkBytes = 4 * 1024 * 1024

// pullRange fetches one byte range.
//
// `tail -c +N` and `head -c M` are used rather than `dd bs=1`, because dd
// with a one-byte block size issues one read syscall per byte — correct, and
// slow enough to matter on a megabyte.
func (t *tokenBackend) pullRange(remotePath string, skip, count int64, timeout time.Duration) ([]byte, error) {
	cmd := fmt.Sprintf("tail -c +%d %s 2>/dev/null | head -c %d",
		skip+1, shellx.Quote(remotePath), count)
	raw, code, err := t.runBinary(cmd, timeout)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("read %d bytes at offset %d of %s: the host command exited %d",
			count, skip, remotePath, code)
	}
	if int64(len(raw)) != count {
		return nil, fmt.Errorf("the host returned %d of %d bytes at offset %d of %s "+
			"(does the file exist and is it that long?)", len(raw), count, skip, remotePath)
	}
	return raw, nil
}

// canDegrade says whether a failed reconnect may be retried on the other
// path.
//
// Only a failure of the MECHANISM qualifies: the token scheme is not
// available here. An asset that cannot be logged into is not one — the
// terminal would refuse it the same way, ten seconds later. And a broken
// connection is not one either: the command may have been delivered, and
// running it again is how `rm -rf` happens twice.
func canDegrade(err error) bool {
	return errors.Is(err, ErrTokenUnavailable)
}

// degrade runs the command on the other path, after the fast path stopped
// working.
//
// It is only called for a command that was NOT delivered, so the command is
// run for the first time here — never a second time.
//
// The answer has to say what actually happened: the session was opened with
// a connection token, but the command ran through the terminal, and an
// answer that says "connection token" would be a lie about the path the
// command took (and about the recording that goes with it).
//
// The account is left UNKNOWN on purpose: the terminal path does not choose
// an account — koko picks it — so the account of the session that opened
// the way must not be reported for a command it did not run.
func (t *tokenBackend) degrade(command string, timeout time.Duration, cause error) (string, int, bool, error) {
	b, err := t.fallback(timeout)
	if err != nil {
		// ⚠️ The state has to travel with this error, and it is known:
		// the command never reached the machine on the token path (that
		// is the only reason this branch exists), and the terminal path
		// could not even open a session. So nothing was sent — on
		// either path — and saying so is what lets the caller know the
		// command is safe to run again.
		//
		// ⚠️ The cause is flattened with %v on purpose: it explains WHY
		// the token path stopped working, and the state of this error is
		// about something else. The inner error is kept in the chain.
		return "", -1, false, notSent(fmt.Errorf(
			"the connection-token path stopped working (%v) and the terminal path could not be used either: %w",
			cause, err))
	}
	defer b.Close()
	out, code, truncated, err := b.Exec(command, timeout)
	if err != nil {
		// The terminal path's own error carries the delivery state (the
		// command was typed into the terminal, so it may be running) —
		// and it is kept in the chain by the %w.
		return "", -1, false, fmt.Errorf(
			"the connection-token path stopped working (%v) and the terminal path failed too: %w",
			cause, err)
	}
	t.mu.Lock()
	t.degraded = &degradation{
		reason:  fmt.Sprintf("the connection-token path stopped working: %v", cause),
		via:     b.Describe(),
		name:    b.Name(),
		account: "",
		// The identity IS known: it is the user the bastion authorised
		// the terminal session as.
		identity: b.Identity(),
	}
	t.degradedToTUI = true
	t.mu.Unlock()
	// The other path is the one that works now: the next session goes
	// there first.
	t.cfg.Memory.Remember(t.cfg.address(), BackendTUI)
	t.remember(command, out)
	return out, code, truncated, nil
}

// Describe says how this session was opened.
func (t *tokenBackend) Describe() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.degraded != nil {
		return t.degraded.via
	}
	return "jumpserver koko (bastion) via a connection token, recorded"
}

// Name is the short form of the same fact.
func (t *tokenBackend) Name() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.degraded != nil {
		return t.degraded.name
	}
	return "connection token"
}

// tokenPathDown says whether this session already knows the token path
// stopped working.
func (t *tokenBackend) tokenPathDown() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.degradedToTUI
}

// Account is the login name the session runs as.
//
// After a degradation it is the account of the OTHER path — and on the
// terminal path that is often unknown (koko picks the account), so it is
// empty rather than a guess.
func (t *tokenBackend) Account() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.degraded != nil {
		return t.degraded.account
	}
	return t.account
}

// Identity is who the session was authorised as on the bastion side.
//
// It comes from the token itself: the bastion stamps the token with the user
// it was minted for, so this is the bastion's own answer rather than our
// guess at it.
func (t *tokenBackend) Identity() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.degraded != nil {
		return t.degraded.identity
	}
	return t.identity
}

// FallbackReason is empty unless the session had to degrade to the other
// path after it was opened.
func (t *tokenBackend) FallbackReason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.degraded != nil {
		return t.degraded.reason
	}
	return ""
}

// Reused says whether this session came from the connection pool.
func (t *tokenBackend) Reused() bool { return t.reused }

// Close hands the connection back to the pool.
//
// It does NOT close it: the next session on the same asset reuses it, and
// that is the whole point of the pool. The pool closes it when it goes
// idle for too long.
func (t *tokenBackend) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	if t.pool != nil {
		t.pool.release(t.conn)
		return
	}
	// A session that opened its own connection closes it here — and closes
	// the keepalive with it.
	if t.conn != nil {
		t.conn.shutdown()
	}
}
