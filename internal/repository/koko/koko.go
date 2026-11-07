package koko

// koko.go drives JumpServer's koko terminal (SSH on port 2222) as a
// programmable session, so an agent can run a command on a managed asset
// through the bastion instead of connecting to the host directly.
//
// Why a TUI driver is necessary: koko only speaks its own full-screen terminal
// UI on 2222. A ConnectionToken (authentication.models.ConnectionToken) looks
// like the natural automation handle, but koko rejects it as an SSH credential
// -- it is for the official jumpserver-client, not for OpenSSH. macOS ssh also
// has no "host:asset" syntax. So the only route is: open a PTY, type the asset
// into koko's search box, confirm, and read the resulting shell.
//
// Verified flow (node-071, 2026-10-14):
//
//	wait for TUI     -> status bar contains "Ctrl+C退出"
//	Ctrl+U, type     -> asset list filters to "1-1 / 1"
//	Enter            -> status bar gains "v详情"
//	Enter            -> "开始连接到opsuser(opsuser)@192.0.2.71", shell prompt
//	command          -> output, then prompt again
//
// Two rendering details drive the implementation: koko repaints the whole
// screen (it embeds tmux), so the useful text is the *last* frame, and a PTY
// with a zero window size never renders at all.

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/logx"
	"github.com/oekit/network/sshx"

	"golang.org/x/crypto/ssh"

	"github.com/yi-nology/jumpserver-kit/pkg/jmsapi"
)

// settleGrace is how long to wait after the end marker first appears before
// starting to check whether the parsed result has actually arrived.
//
// It is an observation window, not a deadline: the loop still returns early once
// two consecutive samples agree, so a fast command costs ~0.6s instead of the
// full window. At 1500ms this dominated every call — four commands through a
// reused session measured 24.7s before the stability check existed and 31.3s
// after. 600ms keeps a margin for the pane to start painting while leaving the
// "two identical samples" guarantee (the part that actually fixes truncation)
// untouched.
const settleGrace = 600 * time.Millisecond

// Markers bracket the command output. They are echoed back through the shell,
// so the region between them can be carved out of the screen paint.
//
// markRC rides inside the piped block rather than after it: the command is piped
// into `cat -n`, so a `$?` read after the pipeline reports cat's status, which is
// always 0.
const (
	markBeginFmt = "__JMSX_BEGIN_%s__"
	markEndFmt   = "__JMSX_END_%s__"
	markRCFmt    = "__JMSX_RC_%s__"
)

const maxScreen = 4 << 20 // 4 MiB

// ptyRows/ptyCols: koko embeds tmux and repaints only what fits the window, so
// these bound how much command output survives a repaint. 50x220 was verified to
// render; much larger requests (200x2000 was tried first) stall the handshake.
const (
	ptyRows = 50
	ptyCols = 220
)

// buffer is a bounded, concurrency-safe rolling capture of the koko screen.
type buffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > maxScreen {
		b.buf = b.buf[len(b.buf)-maxScreen:]
	}
	return len(p), nil
}

// Reset drops everything captured so far. It is called just before a command is
// sent so the following bytes are exactly that command's output, instead of
// several hundred KB of pane repaints that preceded it.
func (b *buffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = b.buf[:0]
}

func (b *buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// Koko is one authenticated koko session, pre-positioned on an asset shell.
type Koko struct {
	Client  *ssh.Client
	Session *ssh.Session
	Stdin   io.WriteCloser
	Stdout  io.Reader
	Screen  *buffer

	Asset   string
	account string
	// user is the JumpServer account the session is authorised as.
	user string

	// epoch is the base for the per-command row numbering; see lineEpochStride.
	epoch int

	// walk is how long the TUI walk took: dialing, waiting for the asset
	// panel, searching, confirming, waiting for the prompt. It is the
	// cost every session pays before any command of the caller's runs,
	// so it is measured rather than assumed.
	walk time.Duration

	// fallbackReason is why this session was opened through the terminal
	// instead of a connection token. It is empty when the terminal was
	// asked for directly.
	fallbackReason string

	// lastOutput is the remote file holding the previous command's raw output.
	// Exec tees there so a result taller than the pane can be re-read in full
	// WITHOUT running the command twice — re-running is not an option, since
	// the command may append, delete, or otherwise have side effects.
	lastOutput string
}

// Config describes how to reach the bastion.
type Config struct {
	Host    string // bastion host
	Port    int    // koko SSH port, 2222
	User    string // JumpServer user (has an SSHKey registered)
	KeyPath string // private key path

	// Backend selects how a session is opened: "auto" (the connection token
	// first, the TUI as a fallback), "token" or "tui". Empty means auto.
	Backend string

	// API is the bastion's REST client. The connection-token backend needs it
	// to resolve the asset and to mint the token; the TUI path does not use
	// it at all.
	API *jmsapi.Client

	// Pool keeps the koko connections open between tool calls.
	//
	// A nil pool means every session opens its own connection, which is
	// correct and costs a handshake each time. The pool turns that into a
	// lookup.
	Pool *Pool

	// Assets resolves asset references, remembering the listing for a while.
	//
	// It is here because a fleet sweep opens one session per host and each
	// session resolves the asset it was given. Without the cache the sweep
	// fetches the whole estate once per host and spends more time listing
	// assets than running commands.
	//
	// A nil resolver is fine: the resolution then fetches the listing every
	// time, which is correct and only slow.
	Assets *jmsapi.AssetResolver

	// Delegation makes the connection-token backend mint the token as the
	// delegated user instead of as the API token's owner.
	//
	// The difference is the authority the session carries: the delegated
	// user is the account the sessions have always run as, while the API
	// token's owner is an administrator on this deployment. A nil value (or
	// one that is not fully configured) means the token is minted with the
	// API token, and the doctor says so.
	Delegation *jmsapi.DelegationConfig

	// Memory remembers which backend worked, so that a bastion where the
	// connection-token scheme is not available does not pay for finding
	// that out again on every call.
	//
	// It is a POINTER because the sessions of a sweep share it: one
	// process, one memory. A nil value means "no memory" — every call
	// then uses the default order, which is the token path first.
	Memory *BackendMemory
}

func (c Config) address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// target describes where the session is going — in terms the operator can
// act on.
//
// ⚠️ The host and the user have NO defaults (a default would point one
// deployment's clients at somebody else's bastion) ⇒ an unset one has to
// say so BY NAME here. The dial error underneath would otherwise read
// "dial koko :2222 as :" — a blank that looks like a typo rather than
// "this was never configured", and sends the operator after the network
// instead of after the configuration.
func (c Config) target() string {
	host := c.Host
	if host == "" {
		host = "(JMS_BASTION_HOST is not set)"
	}
	user := c.User
	if user == "" {
		user = "(JMS_KOKO_USER is not set)"
	}
	return fmt.Sprintf("%s:%d as %s", host, c.Port, user)
}

// OpenTUI dials koko and drives its TUI onto the given asset, returning a
// session whose shell is ready for commands.
//
// It is the fallback backend. Open picks it when the connection-token path is
// unavailable or refused — see backend.go for why the choice between them is
// made before anything is executed and never after.
func OpenTUI(ctx context.Context, cfg Config, asset, account string, timeout time.Duration) (*Koko, error) {
	started := time.Now()
	// ⚠️ An unset host is refused BEFORE anything is dialled.
	//
	// The address would be ":2222", and Go reads an empty host as THIS
	// machine ⇒ the terminal fallback would hand the koko private key to
	// whatever listens on this box's 2222 — a machine that is not the
	// bastion. The host has no default on purpose (a default would point
	// one deployment's clients at somebody else's bastion), so an unset one
	// has to be an error rather than a localhost.
	if cfg.Host == "" {
		return nil, fmt.Errorf("the koko terminal backend needs JMS_BASTION_HOST " +
			"(the host running koko, usually the same host as JUMPSERVER_URL); it is not set")
	}
	// The key is read and parsed by the shared SSH primitive. It is the same
	// code that answers "what type is this key" -- and a wrong-type key
	// looks exactly like a permission problem, so having one place that
	// knows the difference is worth the indirection.
	signer, err := sshx.LoadSigner(cfg.KeyPath)
	if err != nil {
		return nil, err
	}

	cc := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	// The connect and the handshake are bounded separately: a port that
	// accepts the connection and then never speaks SSH would otherwise
	// hang this call forever — and the fallback is already out of the
	// picture, because the fallback IS this path.
	client, err := sshx.Dial(ctx, cfg.address(), cc, sshx.DialOptions{Budget: timeout, Handshake: 15 * time.Second})
	if err != nil && isHandshakeFailure(err) {
		// 握手级失败重拨一次：连接从未建立、什么都没投递 ⇒ 安全。
		// （判据与 mint.go 共用；tailscale 链路上握手超时是常态抖动。）
		client, err = sshx.Dial(ctx, cfg.address(), cc, sshx.DialOptions{Budget: timeout, Handshake: 15 * time.Second})
	}
	if err != nil {
		return nil, fmt.Errorf("dial koko %s: %w", cfg.target(), err)
	}

	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("new session: %w", err)
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.ECHONL:        1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty("xterm", ptyRows, ptyCols, modes); err != nil {
		client.Close()
		return nil, fmt.Errorf("request pty: %w", err)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := sess.Shell(); err != nil {
		client.Close()
		return nil, fmt.Errorf("shell: %w", err)
	}

	screen := &buffer{}
	// The screen is read in the background for the whole session, so a panic
	// here would be a panic with nobody watching: async turns it into a
	// logged stack instead of a dead server.
	async.GoSafeLabeled("koko-stdout", func() {
		if _, err := screen.ReadFrom(stdout); err != nil {
			logx.Error("koko stdout ended", "err", err)
		}
	})

	k := &Koko{
		Client: client, Session: sess, Stdin: stdin, Stdout: stdout,
		Screen: screen, Asset: asset, account: account, user: cfg.User,
	}

	if err := k.selectAsset(timeout); err != nil {
		k.Close()
		return nil, err
	}
	// The walk is over: the shell is ready and the caller's first command
	// can go out. Only a completed walk is a sample -- a failed one says
	// nothing about what a session costs.
	k.walk = time.Since(started)
	recordWalk(k.walk, tagTUI)
	return k, nil
}

// WalkDuration is how long the TUI walk took to reach the asset's shell.
//
// It is the price every session pays before any command runs, and the
// number a caller planning a fleet sweep needs.
func (k *Koko) WalkDuration() time.Duration { return k.walk }

// UploadChunkBytes is how much one upload command may carry.
//
// The pane bounds it: a typed line has to survive the terminal, and the pane
// is 220 columns wide.
func (k *Koko) UploadChunkBytes() int { return uploadChunkBytes }

// PutBytes is not supported on the terminal path.
//
// The TUI types into a screen, so a payload has to survive a line of text:
// the base64 path exists for that reason. The exec channel carries the bytes
// on stdin instead, which is why the token backend has no such limit.
func (k *Koko) PutBytes(remotePath string, data []byte, timeout time.Duration) error {
	return ErrChannelTransferUnsupported
}

// CanCarryBytes says whether this backend can carry file bytes on its
// command channel. The terminal path cannot.
func (k *Koko) CanCarryBytes() bool { return false }

// AppendBytes is not supported on the terminal path, for the same reason as
// PutBytes.
func (k *Koko) AppendBytes(remotePath string, data []byte, timeout time.Duration) error {
	return ErrChannelTransferUnsupported
}

// RunInput is not supported on the terminal path: a typed screen cannot
// carry a binary stream on stdin. Directory upload is honest about this
// instead of degrading to something lossy.
func (k *Koko) RunInput(command string, stdin io.Reader, timeout time.Duration) (int, error) {
	return 0, ErrChannelTransferUnsupported
}

// RunOutput: the TUI path types into a screen and cannot stream a command's
// binary stdout off it — same honesty rule as RunInput.
func (k *Koko) RunOutput(command string, stdout io.Writer, timeout time.Duration) (int, error) {
	return 0, ErrChannelTransferUnsupported
}

// PullBytes reads a byte range of a remote file through the pane.
//
// The pane is the transport, so the bytes come back in base64 chunks sized
// to fit one screen. The token path does the same job in one round trip.
func (k *Koko) PullBytes(remotePath string, start, total int64, timeout time.Duration) ([]byte, error) {
	return k.pullBytesFrom(remotePath, start, total, timeout)
}

// Describe says how this session was opened.
//
// It is what goes into the answer's `via` field, so a caller can tell a
// session that walked the terminal from one that used a connection token.
func (k *Koko) Describe() string {
	return "jumpserver koko (bastion), recorded"
}

// Name is the short form of the same fact.
func (k *Koko) Name() string { return "koko TUI" }

// Identity is who the session was authorised as on the bastion side.
//
// The TUI path logs in as a JumpServer user with an SSH key, so the identity
// is that user.
func (k *Koko) Identity() string { return k.user }

// Account is the login name the session runs as.
//
// The TUI path knows it only when the caller named one: koko picks the
// account itself when the terminal asks, and the answer says so by being
// empty rather than by guessing.
func (k *Koko) Account() string { return k.account }

// Reused is always false here: the terminal path opens its own session
// every time — there is no pool behind it.
func (k *Koko) Reused() bool { return false }

// FallbackReason is why the terminal was used instead of a connection token.
//
// It is reported because a session that took the slow path is worth
// explaining: the fast path was unavailable, and the operator is the one
// who can fix that.
func (k *Koko) FallbackReason() string { return k.fallbackReason }

// promptRe matches a shell prompt such as "opsuser@node:~ $".
//
// Three things about the real screen forced this shape. koko paints the pane
// inside a tmux frame, so the prompt is preceded by one or more frame bars
// rather than the start of the line. The prompt path can contain spaces
// ("~ $"), so the gap up to the "$"/"#" sigil is matched loosely but bounded.
// And the sigil is immediately followed by a frame bar, not a space, so nothing
// may be required after it -- the user@host: prefix is what keeps ordinary text
// such as "bob@example.com" from matching.
var promptRe = regexp.MustCompile(`(?m)[│┃║]*[\w.\-]+@[\w.\-]+:[^\n]{0,40}?[$#]`)

// koko's asset panel changed its wording between versions, and every marker
// this server depends on moved with it. Measured 2026-10-25 against the live
// bastion:
//
//	before                          now
//	------------------------------  -----------------------------------------
//	hint bar ends "Ctrl+C退出"       hint bar ends "q:退出", starts "enter:连接"
//	typing focused the search box    the box is opened with "/"
//	"清空搜索" hint                  "x:清除搜索" appears once a search is active
//	"没有匹配资产"                    "没有匹配的资产"
//	                                 "正在连接 <user>(<user>)@<address>" names the
//	                                   asset that was actually selected
//
// Waiting for the old wording burned the whole timeout on every single asset,
// which looks exactly like "the bastion is down" while the bastion is fine.
const (
	// panelHint is the asset panel's key hint bar. It is the readiness
	// signal: it is present as soon as the panel has painted.
	panelHint = "enter:连接"
	// searchOpen is the title of the asset panel's search box. It is matched
	// loosely ("搜索:" would do) because the spacing around the separator is
	// a rendering detail that has already changed once.
	searchOpen = "搜索:"
	// searchClear is the hint that appears only after a search has been
	// executed, so it is the proof that the filter was applied.
	searchClear = "x:清除搜索"
	// connecting is the banner shown while a session is being opened. It
	// names the asset and address that were actually selected, which is what
	// makes it possible to notice that the wrong asset was picked.
	connecting = "正在连接"
)

// negative markers that abort instead of retrying.
var (
	noAccount = "没有可用的授权账号"
	noAsset   = "没有匹配的资产"
	loadAcct  = "加载账号和协议"
)

func (k *Koko) send(s string) error {
	_, err := io.WriteString(k.Stdin, s)
	return err
}

// assetCounterRe matches the row counter koko paints in the asset panel,
// for example "1-35/49": the rows currently shown and the total asset count.
//
// It is the readiness signal because the panel's hint bar is not dependable.
// Measured 2026-10-25 on this bastion: the asset table paints, the counter
// appears, and the status line keeps reading "正在加载资产…" — the "Ctrl+C"
// hint bar that older deployments show never appears at all. Waiting for it
// burned the entire timeout on every single asset and then failed, which is
// exactly the kind of failure that looks like "the bastion is down" while the
// bastion is fine.
var assetCounterRe = regexp.MustCompile(`(\d+)-(\d+)/(\d+)`)

// counterTotal returns the total asset count from the most recent counter in
// the screen, or 0 when no counter is present.
func counterTotal(s string) int {
	matches := assetCounterRe.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return 0
	}
	last := matches[len(matches)-1]
	n, err := strconv.Atoi(last[3])
	if err != nil {
		return 0
	}
	return n
}

// waitForPanel waits until the asset panel has painted.
//
// Two signals are accepted because the panel paints differently across koko
// versions: the row counter ("1-34/49") and the key hint bar ("enter:连接").
func (k *Koko) waitForPanel(timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		s := stripANSI(k.Screen.String())
		if counterTotal(s) > 0 || strings.Contains(s, panelHint) {
			return s, nil
		}
		if time.Now().After(deadline) {
			return s, fmt.Errorf("timeout %s waiting for the asset panel to paint; screen tail: %s",
				timeout, tail(s, 400))
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// openSearchBox opens koko's asset search box.
//
// The box is opened with "/", and the panel can still be swallowing input for
// a moment after it paints, so the keystroke is sent with a settle delay and
// retried a few times. The marker is the box title, matched loosely: the
// exact spacing around the separator is a rendering detail, not a contract.
func (k *Koko) openSearchBox(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	// The panel needs a moment before it takes input.
	time.Sleep(1200 * time.Millisecond)
	for attempt := 1; attempt <= 4; attempt++ {
		if err := k.send("/"); err != nil {
			return err
		}
		if err := k.waitForText(6*time.Second, searchOpen); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(800 * time.Millisecond)
	}
	s := stripANSI(k.Screen.String())
	return fmt.Errorf("the koko search box did not open after 4 attempts; screen tail: %s", tail(s, 400))
}

// waitForText waits until the given text shows up on the screen.
func (k *Koko) waitForText(timeout time.Duration, want string) error {
	deadline := time.Now().Add(timeout)
	for {
		s := stripANSI(k.Screen.String())
		if strings.Contains(s, want) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout %s waiting for %q; screen tail: %s",
				timeout, want, tail(s, 400))
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func (k *Koko) selectAsset(timeout time.Duration) error {
	// 1. Wait for the asset panel to render.
	screen, err := k.waitForPanel(timeout)
	if err != nil {
		return fmt.Errorf("koko TUI did not start: %w", err)
	}
	beforeTotal := counterTotal(screen)

	// 2. Open the search box and type the keyword.
	//
	// The box is opened with "/" -- typing straight into the panel does
	// nothing, because the panel rather than the box has focus until "/" is
	// pressed. On the version this was written against, Ctrl+U happened to
	// focus the box as a side effect; it no longer does.
	//
	// The panel also paints before it is ready to take input, so the first "/"
	// can be swallowed entirely. It is sent with a settle delay and retried:
	// a lost keystroke here used to cost the whole timeout and then report a
	// timeout that looked like the bastion being down.
	if err := k.openSearchBox(timeout); err != nil {
		return err
	}
	if err := k.send(k.Asset); err != nil {
		return err
	}
	// The keyword has to be visible in the box before the search is run;
	// otherwise the Enter would execute an empty search and the next Enter
	// would select the first asset in the list.
	if err := k.waitForText(timeout, "搜索:"+k.Asset); err != nil {
		return fmt.Errorf("the keyword %q never reached the koko search box: %w", k.Asset, err)
	}

	// 3. Run the search.
	if err := k.send("\r"); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for {
		s := stripANSI(k.Screen.String())
		if strings.Contains(s, noAsset) {
			return fmt.Errorf("koko cannot see any asset matching %q: check the name, "+
				"and that the koko user has an asset permission covering it", k.Asset)
		}
		if strings.Contains(s, searchClear) {
			break
		}
		if total := counterTotal(s); beforeTotal > 0 && total > 0 && total < beforeTotal {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("search for %q never took effect in koko; screen tail: %s",
				k.Asset, tail(s, 400))
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 4. Confirm until a shell prompt appears. The first Enter selects the
	// filtered row, the next accepts the authorized account and opens the
	// session; a third may be needed if the account list is longer than one.
	// A shell prompt is the only success signal.
	var last string
	for attempt := 1; attempt <= 6; attempt++ {
		if err := k.send("\r"); err != nil {
			return err
		}
		time.Sleep(2500 * time.Millisecond)

		last = stripANSI(k.Screen.String())
		if promptRe.MatchString(last) {
			// The banner names the asset that was actually connected to.
			// If it is not the one that was asked for, the search matched
			// something else and the caller must not be told it succeeded.
			if !strings.Contains(last, k.Asset) {
				return fmt.Errorf("koko connected to a different asset than %q (screen says %q); "+
					"the search matched the wrong row", k.Asset, connectedAsset(last))
			}
			return nil
		}
		if strings.Contains(last, noAccount) {
			return fmt.Errorf("no authorized account or terminal protocol for %q: "+
				"the asset permission needs accounts=[\"@ALL\"] (an empty accounts "+
				"list means NO accounts), and the asset needs an ssh protocol", k.Asset)
		}
		if strings.Contains(last, noAsset) {
			return fmt.Errorf("koko cannot see any asset matching %q: check the name, "+
				"and that the koko user has an asset permission covering it", k.Asset)
		}
	}
	return fmt.Errorf("no shell prompt on %q after 6 confirms; screen tail: %s",
		k.Asset, tail(last, 500))
}

// Exec runs one command on the asset and returns its combined output plus the
// exit code parsed from the trailing marker.
//
// The command is piped through `cat -n` on purpose. koko's tmux pane packs
// short lines side by side, so without line numbers a two-line result arrives as
// one scrambled row and the original ordering is unrecoverable. The numbers give
// parseOutput a key to reassemble the rows.
// spaceMark stands in for every space in the command output while it crosses the
// terminal.
//
// koko renders the asset session through tmux, and tmux treats spaces in the
// pane as column padding and swallows them: `echo hello world` arrives as
// `helloworld`, and `ls -la` loses every column separator. Spaces in koko's own
// chrome survive, so this is specific to the pane.
//
// ASCII 0x1E (record separator) is used because it cannot occur in ordinary
// output, unlike a printable stand-in such as '~', which would corrupt any path
// containing a tilde.
const spaceMark = "\x1e"

// spaceMarkEscape is how spaceMark is spelled on the *sending* side. The raw
// 0x1E byte must not be embedded in the command line: it is a control character
// that the shell will not pass through cleanly. tr's octal escape does the job,
// and must agree with spaceMark for the parser to recognise it.
const spaceMarkEscape = `\036`

// lineEpochStride is added to the row counter before every command.
//
// `cat -n` restarts at 1 for each invocation, so on a shared shell a repaint of
// the previous command's rows is indistinguishable from this one's -- four
// different commands came back as four copies of the first answer. Prefixing
// every row with a counter that only moves forward makes the numbering unique
// for the life of the session, so old scrollback sorts below the current
// command instead of colliding with it.
const lineEpochStride = 1000

func (k *Koko) Exec(command string, timeout time.Duration) (string, int, bool, error) {
	return k.exec(command, timeout, true)
}

// exec is Exec with control over the tee artefact.
//
// `capture` must be false for this tool's own bookkeeping commands (measuring a
// file, pulling a chunk back). Those go through the same pane, so tee'ing them
// would overwrite lastOutput with the bookkeeping command's own output — and a
// subsequent full read would then return the size of the file instead of the
// file. Only the caller's command is worth keeping.
func (k *Koko) exec(command string, timeout time.Duration, capture bool) (string, int, bool, error) {
	m := newMarks()
	k.epoch += lineEpochStride
	epoch := k.epoch

	// Drop the previous artefact only when this run is replacing it. Without
	// this a reused session accumulates one capture file per command and only
	// the newest is ever cleaned up. It must NOT happen for capture=false: those
	// runs are the tool measuring or pulling lastOutput, and deleting it there
	// means measuring an already-unlinked file and reading zero bytes.
	rawFile := ""
	if capture {
		if k.lastOutput != "" {
			_ = k.send(fmt.Sprintf("rm -f %s\n", k.lastOutput))
		}
		// Tee the raw stream to a file before the numbering pipeline mangles it.
		// That file is the only place the complete result survives, because the
		// pane only ever shows the last ~40 rows; `full=true` reads it back in
		// chunks. Re-running the command instead is not an option: it may append,
		// delete or otherwise have side effects, so the answer has to come from
		// this run.
		rawFile = fmt.Sprintf("/tmp/.jmsx-out-%d-%d", epoch, os.Getpid())
		k.lastOutput = rawFile
	}

	tee := ""
	if rawFile != "" {
		tee = "tee " + rawFile + " | "
	}
	wrapped := fmt.Sprintf(
		"{ echo %s; %s; echo \"%s\"$?; } 2>&1 | "+tee+"cat -n | "+
			"sed 's/^ *\\([0-9][0-9]*\\)/%d-\\1/' | tr '\\t ' '@%s'; echo %s\n",
		m.begin, command, m.rc, epoch, spaceMarkEscape, m.end)

	// Start from an empty capture: everything before this point is pane
	// repaint noise from navigating the TUI, and searching it for the marker
	// both wastes memory and risks matching a stale one.
	k.Screen.Reset()
	before := 0

	if err := k.send(wrapped); err != nil {
		// The command never reached the terminal, so it never reached the
		// machine: running it again is safe.
		return "", -1, false, notSent(fmt.Errorf("send command: %w", err))
	}

	deadline := time.Now().Add(timeout)
	settle := time.Time{}  // when this command's end marker first showed up
	lastRows := -1         // row count on the previous poll
	lastCode := -1         // exit code seen on the previous poll
	lastPaint := uint64(0) // fingerprint of the rows' text
	stable := 0            // consecutive polls that agreed on both
	var screen string
	for {
		cur := k.Screen.String()

		// The deadline is checked unconditionally: koko can repaint forever
		// after the marker appears, and a check inside one branch only would
		// spin indefinitely in the other.
		if time.Now().After(deadline) {
			fresh := stripANSI(cur[min(before, len(cur)):])
			// The command WAS typed into the shell, so it may well be
			// running on the machine right now. It is never replayed.
			//
			// ⚠️ The wording has to say so: the agent reads this text and
			// decides what to do next, and a failure that does not say
			// "it may still be running" is one the agent retries — which
			// is how `rm -rf` runs twice. The token path says it; this
			// path must say it too.
			return "", -1, false, maybeSent(fmt.Errorf(
				"the command may already be running on the machine: it did not finish within %s. "+
					"If it ends the shell (a bare 'exit'), "+
					"the end marker never appears -- wrap it in a subshell, e.g. sh -c 'exit 42'. "+
					"Partial screen: %s",
				timeout, tail(fresh, 400)))
		}

		// Only look at bytes produced by THIS command, and only at a *real*
		// marker rather than the terminal's echo of what we typed.
		//
		// The PTY echoes the command line itself, and that line contains the
		// literal text `echo __JMSX_END_...`, so a plain substring test fires
		// the instant the command is sent -- before it has run.
		fresh := cur[min(before, len(cur)):]
		if !strings.Contains(fresh, m.end) {
			settle = time.Time{}
			lastRows, lastCode, lastPaint = -1, -1, 0
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// koko keeps repainting after the last byte arrives, and parsing
		// mid-repaint yields a half-painted row.
		if settle.IsZero() {
			settle = time.Now()
		}

		// Wait for the capture to STOP GROWING, not merely to be non-empty.
		//
		// The end marker is emitted outside the pipe, so it can appear while the
		// numbered payload is still being painted; and a wide payload reaches
		// the screen slower than a narrow one. Accepting the first non-empty
		// parse is what made a 3292-byte transfer come back as 1645 bytes —
		// every chunk short by roughly half, with nothing in the result to say
		// so. Two consecutive identical signatures is the cheapest evidence that
		// painting has caught up.
		//
		// The signature includes the exit code because a command with no output
		// at all (`false`) has zero rows forever; waiting on rows alone would
		// hang it until the deadline instead of returning exit_code 1.
		if time.Since(settle) >= settleGrace {
			n, code, paint := resultSignature(cur, m, epoch)
			if (n > 0 || code >= 0) && n == lastRows && code == lastCode && paint == lastPaint {
				stable++
				if stable >= 2 {
					screen = cur
					break
				}
			} else {
				stable = 0
			}
			lastRows, lastCode, lastPaint = n, code, paint
		}
		time.Sleep(250 * time.Millisecond)
	}

	body := screen[min(before, len(screen)):]
	// The RAW body, not the stripped one: the row matcher needs the escape
	// sequences to know where a row starts (see lastNumberedStart).
	out, code, truncated, err := parseOutput(body, m, epoch)
	if err != nil {
		// ⚠️ NOT classified, on purpose. The end marker WAS on the
		// screen (the loop only breaks once it is there and the paint
		// has settled), so the command ran to completion — only the
		// OUTPUT could not be read back. Calling that "maybe sent" would
		// put a finished command into the "may still be running"
		// collection, and that collection has to be trustworthy: it is
		// what stops somebody from replaying a command.
		//
		// "Nobody said" is the honest answer here.
		return out, code, truncated, err
	}
	return out, code, truncated, nil
}

// PullCommandOutput reads back the complete output of the last Exec, from the
// file Exec tee'd on that run.
//
// The pane keeps ~40 rows, so anything taller is simply gone from the screen.
// Re-running the command to recover it would be wrong for any command with side
// effects, so this reads the artefact of the run that already happened.
func (k *Koko) PullCommandOutput(command string, timeout time.Duration) (string, error) {
	if k.lastOutput == "" {
		return "", fmt.Errorf("no output was captured for a re-read; run the command first")
	}
	sizeOut, _, _, err := k.exec(fmt.Sprintf("wc -c < %s | tr -d ' '", k.lastOutput), timeout, false)
	if err != nil {
		return "", fmt.Errorf("measure captured output: %w", err)
	}
	var size int64
	fmt.Sscanf(strings.TrimSpace(sizeOut), "%d", &size)
	if size <= 0 {
		return "", fmt.Errorf("the captured output is empty (no tee artefact for %s)", command)
	}

	raw, err := k.pullBytes(k.lastOutput, size, timeout)
	if err != nil {
		return "", err
	}
	if int64(len(raw)) != size {
		// The pane could not hold the whole capture even after the chunker
		// shrank. Say how much is missing rather than returning a short answer
		// that reads as complete.
		return "", fmt.Errorf("recovered %d of %d bytes of the captured output; "+
			"re-run with a command that produces less (pipe through head/tail) "+
			"or narrow it with a filter", len(raw), size)
	}
	// The tee captures the pre-numbering stream, which still contains the begin
	// and rc markers this tool synthesised. They are scaffolding, not output.
	return stripScaffolding(string(raw), command), nil
}

func (k *Koko) Close() {
	// Remove the tee artefact before dropping the shell, so a big capture does
	// not stay in the asset's /tmp. Failures here are irrelevant — the file is
	// already gone as far as this process is concerned.
	if k.lastOutput != "" {
		io.WriteString(k.Stdin, fmt.Sprintf("rm -f %s\n", k.lastOutput))
		time.Sleep(200 * time.Millisecond)
	}
	// Best effort: drop the shell, then the transport.
	io.WriteString(k.Stdin, "exit\n")
	time.Sleep(300 * time.Millisecond)
	if k.Session != nil {
		k.Session.Close()
	}
	if k.Client != nil {
		k.Client.Close()
	}
}
