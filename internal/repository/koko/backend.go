package koko

// backend.go chooses HOW a session on an asset is opened.
//
// There are two ways to reach a machine through the bastion, and they answer
// the same question at very different prices:
//
//   - the TUI walk (koko.go): dial koko, wait for the asset panel, open the
//     search box, type the name, confirm, wait for the prompt. Measured on this
//     fleet: 7.25 to 9.12 seconds before a single byte of the caller's command
//     is sent.
//
//   - the connection token (token.go): ask the bastion for a one-shot
//     credential, then log into koko with it and open an exec channel.
//     Measured: 239ms to mint the token and 205ms to complete the handshake.
//
// Both end up on the same machine as the same account, and both are recorded
// in the bastion's audit trail. The token path is six to seven times cheaper
// and it returns the command's exit code natively instead of reconstructing it
// from a marker in the painted screen.
//
// Why both are kept: the token path depends on an API endpoint and an account
// UUID, and a bastion that does not offer them (or a deployment whose
// delegation secret is not configured) must still be able to run commands.
// The TUI path is that fallback — and it is only ever used when NOTHING has
// been executed yet, because a command that may have reached the machine must
// never be replayed on another path.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oekit/pkg/slicex"

	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/jmsapi"
)

// The backend modes.
const (
	// BackendAuto tries the connection token first and falls back to the TUI.
	BackendAuto = "auto"
	// BackendToken only uses the connection token.
	BackendToken = "token"
	// BackendTUI only walks the koko terminal.
	BackendTUI = "tui"
)

// ErrCommandTimeout is a command that did not finish in time.
//
// It is its own error because it decides whether a retry is allowed. A
// command that TIMED OUT was delivered to the machine and may still be
// running there: `rm -rf` does not become harmless because the client gave
// up waiting for it. So a timeout must never be replayed, while a failure to
// OPEN the channel never reached the machine and may be.
//
// It carries that state as well (maybe_sent), and says so in its own
// wording: the sentence an operator reads is the one that decides whether
// running it again is safe.
var ErrCommandTimeout = &deliveryError{
	text:     "the command timed out (it may still be running on the machine)",
	delivery: jms.Delivery_maybe_sent,
}

// ErrTokenUnavailable says the connection-token scheme itself is not
// available here: the endpoint is not there, the deployment does not offer
// it, or the API client is missing.
//
// It is what decides whether the TUI fallback is worth trying. A failure
// about the ASSET — no account on it, no permission covering it — would fail
// the same way through the terminal, and walking the terminal to find that
// out costs ten seconds of everybody's time. Only a failure of the
// MECHANISM is worth the walk.
var ErrTokenUnavailable = errors.New("the connection-token scheme is not available here")

// ErrChannelTransferUnsupported says the backend cannot carry file bytes on
// the command channel.
//
// The koko TUI types into a terminal, so its payload has to be short
// enough to survive a line of text — which is why it needs base64 and
// chunks. The exec channel carries the bytes on stdin instead, which is
// why the token backend has no such limit.
var ErrChannelTransferUnsupported = errors.New("this backend cannot carry file bytes on the command channel")

// Backend is a session on an asset.
//
// The tool layer talks to this and never to an implementation, so a new way
// of opening a session is a new implementation rather than a new call
// convention.
type Backend interface {
	// Exec runs one command and returns its combined output and its exit
	// code. The third value says the output was cut short; the token backend
	// never cuts it short and always answers false.
	Exec(command string, timeout time.Duration) (out string, code int, truncated bool, err error)

	// PullCommandOutput re-reads the complete output of the last Exec.
	//
	// It exists because the TUI path can lose the top of a long result to
	// the pane's height. The token path has no such limit, so it answers
	// with what it already has.
	PullCommandOutput(command string, timeout time.Duration) (string, error)

	// UploadChunkBytes is how many base64 characters of a file one upload
	// command may carry.
	//
	// The TUI path is bounded by the terminal: a typed line survives up to
	// about 2668 characters, and the pane is 220 columns wide. The token
	// path has neither limit, so it carries far more per round trip — and
	// a file that took hundreds of round trips over the pane takes a
	// handful over the wire.
	UploadChunkBytes() int

	// PutBytes writes a file on the asset, replacing what is there.
	//
	// The token backend writes the bytes on the exec channel's stdin: one
	// round trip, no command-line length limit, no base64 expansion. The
	// TUI backend types into a terminal and cannot do it, so it answers
	// ErrChannelTransferUnsupported and the caller falls back to the
	// base64 path.
	PutBytes(remotePath string, data []byte, timeout time.Duration) error

	// CanCarryBytes says whether this backend can carry file bytes on its
	// command channel.
	//
	// The terminal path cannot: it types into a screen, so a payload has to
	// survive a line of text. A relay needs it — the bytes go from one
	// machine's channel straight onto another's.
	CanCarryBytes() bool

	// AppendBytes writes bytes at the END of a file on the asset.
	//
	// It is what a chunked transfer needs after its first chunk: the first
	// chunk replaces the file and the rest extend it.
	AppendBytes(remotePath string, data []byte, timeout time.Duration) error

	// PullBytes reads a byte range of a remote file.
	//
	// The transfer machinery needs bytes rather than text, and the two
	// backends get them differently: the TUI path has to squeeze them
	// through a 40-row pane in base64 chunks, while the token path reads
	// them straight off the wire in one round trip.
	PullBytes(remotePath string, start, total int64, timeout time.Duration) ([]byte, error)

	// RunInput runs a command with a byte stream on its stdin — the shape a
	// streamed upload needs (`tar -xf - -C dir` drinking the archive).
	//
	// The token backend feeds the channel's stdin; the TUI path types into
	// a screen and cannot carry a binary stream, so it answers
	// ErrChannelTransferUnsupported and the caller reports that honestly
	// instead of falling back to something lossy.
	RunInput(command string, stdin io.Reader, timeout time.Duration) (exitCode int, err error)

	// RunOutput is RunInput's mirror: a command whose STDOUT is the payload
	// (`tar -cf - -C dir` writing the archive), streamed straight into the
	// writer — a directory download never holds the whole archive in
	// memory. Same honesty rule as RunInput: the TUI path answers
	// ErrChannelTransferUnsupported.
	RunOutput(command string, stdout io.Writer, timeout time.Duration) (exitCode int, err error)

	// WalkDuration is what opening this session cost.
	WalkDuration() time.Duration

	// Describe says how this session was opened. It goes into the answer's
	// `via` field, so a caller can tell which path produced it.
	Describe() string

	// Name is the short form of the same fact: "connection token" or
	// "koko TUI". It is what the session statistics report.
	Name() string

	// Account is the login name the session runs as, when the backend knows
	// it. The token backend resolves it before connecting; the TUI path
	// leaves it empty when the caller did not name one.
	Account() string

	// Reused says whether this session came from the connection pool
	// rather than being opened from scratch.
	//
	// It is the difference between a session that paid for a token and a
	// handshake and one that did not, and the answer says which.
	Reused() bool

	// FallbackReason is why this backend was used instead of the preferred
	// one. It is empty when the preferred backend was used, and it says why
	// the fast path was skipped when it was not.
	FallbackReason() string

	// Identity is who the session was authorised as on the BASTION side.
	//
	// It is not the same thing as Account: Account is the login on the
	// machine, Identity is the JumpServer user whose rights the session
	// carries. The two are deliberately different, and the difference is
	// worth reporting — a session authorised as an administrator reaches
	// more than one authorised as the `ai` account.
	Identity() string

	// Close drops the session.
	Close()
}

// Open opens a session on an asset.
//
// The fallback from the token path to the TUI path happens HERE and only
// here: nothing has been executed at this point, so trying the other way is
// free of the replay hazard. Once a command has been sent, the backend that
// sent it is the one that has to finish the job.
//
// The fallback is also CONDITIONAL: it happens when the token scheme itself
// is unavailable, not when the asset cannot be logged into. An asset with no
// account fails the same way through the terminal, and walking the terminal
// to find that out costs ten seconds per host — a fleet sweep of broken
// hosts would pay it over and over.
func Open(ctx context.Context, cfg Config, asset, account string, timeout time.Duration) (Backend, error) {
	var (
		b   Backend
		err error
	)
	switch strings.ToLower(strings.TrimSpace(cfg.Backend)) {
	case BackendToken:
		b, err = openToken(ctx, cfg, asset, account, timeout)
	case BackendTUI:
		b, err = openTUI(ctx, cfg, asset, account, timeout)
	case "", BackendAuto:
		b, err = openAuto(ctx, cfg, asset, account, timeout)
	default:
		// A configuration mistake — but the delivery fact is still true:
		// no session was opened, so no command left this process. It goes
		// through the same path as every other failure to open (below),
		// so the classification is not an accident of the switch.
		return nil, fmt.Errorf("unknown session backend %q: use %q, %q or %q",
			cfg.Backend, BackendAuto, BackendToken, BackendTUI)
	}
	if err != nil {
		// A session that never opened means the command never left this
		// process — so the delivery state is known without guessing.
		//
		// ⚠️ That includes a CONFIGURATION mistake (an unknown backend
		// name): the fact is still true, and saying it is deliberate —
		// a sweep that failed this way sent nothing and is safe to run
		// again. And "no, it never left" is what lets an operator tell
		// "this host never even started" from "this host may still be
		// running it".
		return nil, notSent(err)
	}
	return b, nil
}

// openAuto opens a session, trying the backends in the order the memory
// gives.
//
// The order is the only thing the memory changes: it never skips a backend
// and it never turns an asset problem into a fallback. A memory that has
// expired (or that was never made) means the default order, which is how a
// backend that started working again is found again.
func openAuto(ctx context.Context, cfg Config, asset, account string, timeout time.Duration) (Backend, error) {
	var failures []error
	// tokenErr is why the token path failed, when it did. The terminal
	// path that takes over says so in its answer: a fallback that hides
	// why it happened looks exactly like a healthy fast path.
	var tokenErr error
	for _, backend := range backendOrder(cfg) {
		b, err := openBackend(ctx, cfg, backend, asset, account, timeout)
		if err == nil {
			cfg.Memory.Remember(cfg.address(), backend)
			if backend == BackendTUI && tokenErr != nil {
				if k, ok := b.(*Koko); ok {
					k.fallbackReason = tokenErr.Error()
				}
			}
			return b, nil
		}
		// The token path falls back to the terminal only when the SCHEME
		// is unavailable. An asset that cannot be logged into would fail
		// the same way through the terminal, ten seconds later.
		if backend == BackendToken {
			tokenErr = err
			if !errors.Is(err, ErrTokenUnavailable) {
				return nil, err
			}
		}
		// The terminal path failing says nothing about the token path, so
		// the other one is tried — and its error is the more precise of the
		// two ("no usable account on the asset" beats "the walk failed").
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		return nil, fmt.Errorf("no session backend was tried")
	}
	// BOTH failures go into the answer: the one that came first is often
	// the actionable one ("the token scheme is not available here"), and
	// a report that only carries the last error hides it.
	return nil, fmt.Errorf("every session backend failed: %w", errors.Join(failures...))
}

// backendOrder is the order the backends are tried in.
//
// The default is the token path first — it is six to seven times cheaper.
// The memory can move the terminal path to the front, and only that: the
// memory says which one worked last time, and a memory that has expired
// says nothing at all, so the fast path is probed again.
func backendOrder(cfg Config) []string {
	order := []string{BackendToken, BackendTUI}
	if cfg.Memory != nil && cfg.Memory.Preferred(cfg.address()) == BackendTUI {
		order = []string{BackendTUI, BackendToken}
	}
	return order
}

// openBackend opens a session with one specific backend.
func openBackend(ctx context.Context, cfg Config, backend, asset, account string, timeout time.Duration) (Backend, error) {
	switch backend {
	case BackendTUI:
		return openTUI(ctx, cfg, asset, account, timeout)
	default:
		return openToken(ctx, cfg, asset, account, timeout)
	}
}

// openTUI walks the koko terminal onto the asset.
//
// The walk costs ten seconds, and an asset with no usable account (or no
// such asset at all) cannot be logged into through it either. So the check
// comes first — it is a lookup in the cached listing.
//
// It fails SOFT when the bastion cannot answer: the API being unreachable
// is exactly the case where the terminal is the only way left, and refusing
// to walk it would leave nothing.
func openTUI(ctx context.Context, cfg Config, asset, account string, timeout time.Duration) (Backend, error) {
	if err := checkAssetUsable(ctx, cfg, asset, account); err != nil {
		return nil, err
	}
	return OpenTUI(ctx, cfg, asset, account, timeout)
}

// checkAssetUsable says whether the asset can be logged into at all.
//
// It exists for the case where the terminal is tried FIRST: without it, a
// sweep that meets one broken asset pays ten seconds for it, which is the
// exact thing the conditional fallback exists to avoid.
//
// Two rules keep it from refusing a machine that would have connected:
//
//   - the accounts are queried FRESH, not from the resolver's five-minute
//     cache. The cache would refuse an asset whose account was granted a
//     moment ago — and the terminal panel asks the bastion LIVE, so the
//     old code connected to it;
//   - an asset the cached listing does not know is NOT a refusal either:
//     it may have been created a moment ago.
//
// What IS a refusal: an asset with no usable account at all (a fresh query
// says so), and a NAMED account that does not exist — the terminal picks
// the account itself, so it would silently run as a DIFFERENT one, and the
// answer would name the one that was asked for.
func checkAssetUsable(ctx context.Context, cfg Config, asset, account string) error {
	if cfg.API == nil {
		return nil
	}
	ref, err := resolveAsset(ctx, cfg, asset)
	if err != nil {
		// The listing may be stale, and the bastion may be unreachable.
		// Neither is a refusal: the walk decides, exactly as it always did.
		return nil
	}
	accts, err := cfg.API.AssetAccounts(ctx, ref.ID)
	if err != nil {
		// The bastion cannot answer right now: not a refusal.
		return nil
	}
	if len(accts) == 0 {
		return fmt.Errorf("%w: a session needs one (an asset permission that grants no account cannot be logged into)", ErrNoAccountOnAsset)
	}
	if account != "" {
		for _, a := range accts {
			if strings.EqualFold(a.Username, account) || strings.EqualFold(a.Name, account) {
				return nil
			}
		}
		names := slicex.Map(accts, func(a jmsapi.AccountRef) string { return a.Username })
		sort.Strings(names)
		return fmt.Errorf("no account %q on that asset (have: %s)", account, strings.Join(names, ", "))
	}
	return nil
}

// mergedBuffer collects stdout and stderr in arrival order.
//
// The two streams are copied by two goroutines inside x/crypto/ssh, so the
// buffer they share MUST be safe for concurrent writes. A plain bytes.Buffer
// is not, and the race does not fail loudly: it shows up as output that
// silently vanishes, so a command exits 0 and prints nothing.
//
// Arrival order is what the caller expects: the TUI path merges the two
// streams in the shell (`2>&1`), and a merged buffer keeps the same shape.
type mergedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (m *mergedBuffer) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.Write(p)
}

// String returns what has been collected so far.
//
// It is safe to call while a copy is still running: the lock makes the read
// atomic, so the result is a prefix rather than a torn buffer.
func (m *mergedBuffer) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.String()
}
