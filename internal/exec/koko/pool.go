package koko

// pool.go keeps the koko connections open between tool calls.
//
// Why it exists. A connection is authenticated ONCE — the connection token
// is single-use — and after that one connection can carry any number of
// exec channels. An MCP server is a long-lived process, so the connections
// can live in it too, and "open a session" stops being an operation that
// costs a round trip to the bastion and a handshake.
//
// The rules that keep it safe, each of them learned the hard way:
//
//   - ONE command at a time per connection. Measured on 2026-10-28: three
//     channels opened concurrently on one connection, and one of them came
//     back with no output at all.
//   - A command that was DELIVERED is never replayed. A command that was
//     not — the channel could not even be opened — may be, once, on a
//     fresh connection. `rm` and `reboot` are not idempotent.
//   - A broken connection is evicted, never reused.
//   - Idle connections are closed. A connection nobody uses again would
//     otherwise hold a session on the bastion forever.
//
// The connection token expires five minutes after it is minted. Whether the
// SESSION survives that is a question about the bastion, not about the
// token, and it is measured rather than assumed — see PoolTTL.

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/concurrency/syncx"
	"github.com/oekit/network/sshx"
	"golang.org/x/crypto/ssh"

	"github.com/yi-nology/jumpserver-mcp-kit/pkg/jmsapi"
)

// PoolTTL is how long an idle connection is kept.
//
// The connection token is good for five minutes, but the token is what
// AUTHENTICATES the session, not what keeps it alive. Measured on
// 2026-10-28: a connection opened at 01:49 still ran commands at 01:56,
// seven minutes on — past the token's lifetime by two. So the TTL is not
// bounded by the token.
//
// It is bounded by what an idle connection COSTS, and that was measured
// rather than guessed (2026-10-28):
//
//   - it does NOT show up in the bastion's session list. A twenty-host
//     sweep leaves the bastion with ZERO unfinished sessions, because
//     what the bastion records is the per-command exec session, and that
//     ends when the command does. The transport connection is invisible
//     to it.
//   - it DOES hold something on the ASSET: one `sshd-session: <user>
//     [priv]` process per idle connection, and one SSH transport to koko.
//
// So the TTL is about how long those processes hang around, not about the
// bastion's session table. The reuse that matters is an agent's burst of
// calls on ONE asset — exec, then put_file, then get_file — which is
// seconds apart. Two minutes covers that and keeps the lingering short.
//
// The operator can raise it; the cost is what is described above.
//
// There is a security reading of the same number, and it is the reason not
// to make it long: a pooled connection carries the authority it was minted
// with. If a permission is revoked, the connections already open keep
// working until they are closed. So the TTL is also how long a revoked
// right stays effective — two minutes here, and the operator can shorten
// it.
const PoolTTL = 2 * time.Minute

// poolSweepInterval is how often the reaper looks for idle connections.
const poolSweepInterval = 60 * time.Second

// Pool is a cache of open koko connections, keyed by the asset and the
// account they run as.
//
// It is safe for concurrent use.
type Pool struct {
	mu    sync.Mutex
	conns map[string]*pooledConn
	// gates serialise the CREATION of a connection per key, so two callers
	// that want the same asset do not open two connections for it. The key
	// space is the fleet, so the map does not grow without bound.
	//
	// A Gate rather than a plain mutex, because its Acquire takes a
	// context: a caller that gives up does not stay queued.
	gates map[string]*syncx.Gate
	// cfg is how a new connection is made. It is a copy of the
	// configuration, and the pointers inside it (the API client, the
	// delegation settings, the asset resolver) are shared.
	cfg  Config
	ttl  time.Duration
	once sync.Once

	// dial opens one new connection.
	//
	// It is a field so that the pool's BOOKKEEPING can be tested without a
	// bastion: who wins a race for the same asset, whether a connection
	// that is in use can be evicted, whether the reaper touches a busy
	// connection. Those are the parts with locks in them, and they are
	// exactly the parts that cannot be reached by calling the real thing.
	dial func(ctx context.Context, cfg Config, asset, account string, timeout time.Duration) (*pooledConn, error)
}

// pooledConn is one open connection and the lock that serialises its use.
type pooledConn struct {
	client *ssh.Client

	// gate serialises the commands on this connection. Two commands on one
	// connection at the same time is what makes output vanish.
	//
	// It is oekit's Gate with a capacity of one: a Gate's Acquire takes a
	// context, so a caller can give up waiting, and its TryAcquire takes
	// the slot or does not — which is what the reaper needs, so that it
	// never blocks on a connection that is busy.
	gate *syncx.Gate

	// lastUse is when the connection was last returned to the pool.
	lastUse time.Time

	// broken marks a connection that failed in a way that says it is no
	// longer usable. It is evicted rather than reused.
	//
	// It is atomic because it is written while the connection's lock is
	// held and read while the POOL's lock is held — two different locks,
	// so an ordinary field would be a race.
	broken atomic.Bool

	// account is the login name the connection runs as, and identity is who
	// it was authorised as on the bastion side. They are fixed when the
	// connection is opened, because the token that bound them is spent.
	account  string
	identity string

	// assetName is the name the caller used for the asset, so the pool can
	// report what it holds in words rather than in UUIDs.
	assetName string

	// done is closed when the connection is closed. It stops the keepalive
	// that keeps the connection warm while it sits idle in the pool.
	done chan struct{}

	// closeOnce keeps the close path single. The pool can reach the same
	// connection from two directions — the reaper and an eviction — and a
	// double close of the done channel would panic.
	closeOnce sync.Once
}

// NewPool returns a pool that makes connections with this configuration.
func NewPool(cfg Config, ttl time.Duration) *Pool {
	if ttl <= 0 {
		ttl = PoolTTL
	}
	// The pool does not need a pool of its own.
	cfg.Pool = nil
	p := &Pool{
		conns: map[string]*pooledConn{},
		gates: map[string]*syncx.Gate{},
		cfg:   cfg,
		ttl:   ttl,
	}
	p.dial = func(ctx context.Context, cfg Config, asset, account string, timeout time.Duration) (*pooledConn, error) {
		return dialPooled(ctx, cfg, asset, account, timeout)
	}
	return p
}

// key identifies a connection: the asset and the account it runs as.
//
// The asset part is the RESOLVED id, not the string the caller typed. Two
// spellings of the same machine — "node-071" and "192.0.2.71" — are one
// machine, and keying on the spelling would give it two connections and
// two idle sshd session processes on it.
func poolKey(assetID, account string) string {
	return assetID + "|" + account
}

// poke says something on the connection, so that whatever sits between this
// machine and koko does not close it while it sits idle.
//
// It takes the connection's lock for the poke, and that is not a nicety: the
// poke goes out on the same write path as the commands, and x/crypto's
// handshake transport holds a lock while it writes. A poke that landed in
// the middle of a command's write would queue behind it — and a poke that
// blocked (a peer that has stopped reading) would hold that lock with it.
//
// A connection that is busy is simply not poked: it is not idle, and it does
// not need warming. The command that is running on it is keeping it warm all
// by itself.
func (c *pooledConn) poke() error {
	release, ok := c.gate.TryAcquire()
	if !ok {
		return sshx.ErrBusy
	}
	defer release()
	_, _, err := c.client.SendRequest(sshx.KeepAliveRequest, false, nil)
	return err
}

// close drops the connection.
//
// It takes the connection's lock FIRST. Another session may be running a
// command on this connection right now, and closing the client under it
// would kill that command half-way — and a command that was delivered may
// not be replayed, so the user would just see a failure with no way to
// retry it.
//
// The lock is what makes closing safe, not what makes it slow: a
// connection nobody is using is free to take.
func (c *pooledConn) close() {
	// The gate is taken first, so that closing never happens under a
	// running command.
	release, err := c.gate.Acquire(context.Background())
	if err != nil {
		return
	}
	defer release()
	c.shutdown()
}

// shutdown closes the connection and stops its keepalive.
//
// It is the close path itself, without the connection's lock — so it must
// only be called once the connection is known to be idle. It is also the
// path a session takes when it opened a connection of its own, outside the
// pool.
func (c *pooledConn) shutdown() {
	c.closeOnce.Do(func() {
		if c.done != nil {
			close(c.done)
		}
		if c.client != nil {
			_ = c.client.Close()
		}
	})
}

// gate returns the per-key lock, creating it if needed.
func (p *Pool) gate(key string) *syncx.Gate {
	p.mu.Lock()
	defer p.mu.Unlock()
	g, ok := p.gates[key]
	if !ok {
		g = syncx.NewGate(1)
		p.gates[key] = g
	}
	return g
}

// get returns a usable connection for the asset, opening one if needed.
//
// The second return value says whether the connection came from the pool:
// a fresh one has paid the handshake, a reused one has not. The answer goes
// into the session's statistics, because it is the difference between a
// sweep that costs one handshake per host and one that costs one per call.
func (p *Pool) get(ctx context.Context, cfg Config, asset, account string, timeout time.Duration) (*pooledConn, bool, error) {
	// The asset is resolved first, because the key is its id. The
	// resolution is cached, so this is a map lookup in the common case.
	ref, err := resolveAsset(ctx, cfg, asset)
	if err != nil {
		return nil, false, err
	}
	// The account is resolved BEFORE the key, so that the key names the
	// account the session will actually run as. Keying on the REQUESTED
	// account would give one machine two connections the moment a caller
	// named an account other than the one the session resolves to — two
	// sshd sessions on it, and the second buys nothing.
	acct, err := pickAccount(ctx, cfg, ref.ID, account)
	if err != nil {
		return nil, false, err
	}
	key := poolKey(ref.ID, acct.Username)

	g := p.gate(key)
	release, err := g.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	defer release()

	p.mu.Lock()
	if c, ok := p.conns[key]; ok && !c.broken.Load() {
		c.lastUse = time.Now()
		p.mu.Unlock()
		p.sweep()
		return c, true, nil
	}
	p.mu.Unlock()

	c, err := p.dial(ctx, cfg, asset, account, timeout)
	if err != nil {
		return nil, false, err
	}
	if c == nil {
		return nil, false, fmt.Errorf("the connection dialer returned no connection")
	}
	c.lastUse = time.Now()
	if c.assetName == "" {
		c.assetName = ref.Name
	}

	p.mu.Lock()
	old := p.conns[key]
	p.conns[key] = c
	p.mu.Unlock()
	if old != nil {
		// Someone else won the race while we were dialling. The old
		// connection is closed OUTSIDE the pool's lock: closing takes the
		// connection's own lock, and that connection may be in use by
		// another session — waiting for it while holding the pool's lock
		// would block every other caller of the pool.
		old.close()
	}

	p.sweep()
	return c, false, nil
}

// release hands a connection back to the pool.
//
// It does NOT close it: that is the whole point of the pool. The caller
// must not use the connection afterwards.
func (p *Pool) release(c *pooledConn) {
	if c == nil {
		return
	}
	p.mu.Lock()
	c.lastUse = time.Now()
	p.mu.Unlock()
}

// evict drops a connection from the pool and closes it.
//
// It is called when the connection failed in a way that says it is no longer
// usable. The next call opens a fresh one.
func (p *Pool) evict(c *pooledConn) {
	if c == nil {
		return
	}
	p.mu.Lock()
	for key, conn := range p.conns {
		if conn == c {
			delete(p.conns, key)
			break
		}
	}
	p.mu.Unlock()
	// The close happens OUTSIDE the pool's lock, and it takes the
	// CONNECTION's lock: another session may be running a command on it,
	// and closing the client under it would kill that command half-way.
	c.close()
}

// sweep closes the connections that have been idle for too long.
//
// It runs at most once per sweep interval no matter how often it is called,
// because the call happens on every session open.
func (p *Pool) sweep() {
	p.once.Do(func() {
		async.GoSafeLabeled("koko-pool-sweep", func() {
			ticker := time.NewTicker(poolSweepInterval)
			defer ticker.Stop()
			for range ticker.C {
				p.reap()
			}
		})
	})
}

// reap closes the connections nobody has used for a while.
//
// A connection that is BUSY is left alone. The idle test is the time since
// it was last handed out, and a command that runs longer than the TTL
// would otherwise be killed by the reaper while it was still going.
func (p *Pool) reap() {
	p.mu.Lock()
	var dead []*pooledConn
	var busy []*pooledConn
	for key, c := range p.conns {
		if time.Since(c.lastUse) <= p.ttl {
			continue
		}
		// TryLock, not Lock: a connection in use is skipped this round
		// rather than waited for, so the reaper never blocks.
		release, ok := c.gate.TryAcquire()
		if !ok {
			busy = append(busy, c)
			continue
		}
		release()
		delete(p.conns, key)
		dead = append(dead, c)
	}
	p.mu.Unlock()
	for _, c := range dead {
		c.close()
	}
	// A connection that was busy when the reaper came by is still there;
	// it will be collected on a later round.
	_ = busy
}

// Holds says whether the pool already has a connection for this asset.
//
// It is how a PLAN learns that some of its hosts are cheaper to visit: a
// sweep of machines this process has already talked to reuses those
// connections and does not pay for a new one.
func (p *Pool) Holds(ctx context.Context, cfg Config, asset, account string) bool {
	if p == nil || cfg.API == nil {
		return false
	}
	ref, err := resolveAsset(ctx, cfg, asset)
	if err != nil {
		return false
	}
	key := poolKey(ref.ID, account)
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.conns[key]
	return ok && !c.broken.Load()
}

// Entry is one connection in the pool, as the status report sees it.
type Entry struct {
	// Asset is the name the caller used for the machine.
	Asset string
	// Account is the login the connection runs as.
	Account string
	// IdleSeconds is how long since the connection was last used.
	IdleSeconds float64
	// Identity is who the connection was authorised as on the bastion side.
	Identity string
}

// Entries reports what the pool holds.
//
// It is what the status tool answers with: a pool with twenty connections in
// it is the answer to "why is my repeat sweep so fast", and also to "what is
// still open on the bastion".
func (p *Pool) Entries() []Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Entry, 0, len(p.conns))
	for _, c := range p.conns {
		out = append(out, Entry{
			Asset:       c.assetName,
			Account:     c.account,
			IdleSeconds: round2(time.Since(c.lastUse).Seconds()),
			Identity:    c.identity,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Asset < out[j].Asset })
	return out
}

// Size reports how many connections are open.
//
// It is what the doctor prints: a pool with twenty connections in it is the
// answer to "why is my repeat sweep so fast", and also to "why does the
// bastion show so many open sessions".
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

// dialPooled opens one connection: resolve the account, mint the token that
// binds it, and log into koko with the token.
func dialPooled(ctx context.Context, cfg Config, asset, account string, timeout time.Duration) (*pooledConn, error) {
	ref, err := resolveAsset(ctx, cfg, asset)
	if err != nil {
		return nil, err
	}
	acct, tok, err := resolveAndMint(ctx, cfg, ref, account)
	if err != nil {
		return nil, err
	}
	client, err := dialKoko(ctx, cfg, tok, timeout)
	if err != nil {
		// A REFUSAL of the token login means this koko does not accept
		// token logins — which is exactly the case the terminal fallback
		// exists for. A network failure means the bastion is unreachable,
		// and the terminal would fail the same way, so there is nothing to
		// gain by trying it.
		if isAuthRefusal(err) {
			// %w and not %v: the refusal's own error has to stay in the
			// chain, or nobody can tell WHICH refusal it was — and
			// "this koko does not accept token logins" is a different
			// thing from "the bastion is unreachable".
			return nil, fmt.Errorf("%w: %w", ErrTokenUnavailable, err)
		}
		return nil, err
	}
	conn := &pooledConn{
		client:    client,
		gate:      syncx.NewGate(1),
		done:      make(chan struct{}),
		account:   acct.Username,
		identity:  tok.User,
		assetName: ref.Name,
	}
	// The keepalive pokes the connection while it sits idle, so that
	// whatever sits between this machine and koko does not close it under
	// us. When it finds the connection gone, it marks it broken and the
	// next call opens a fresh one.
	sshx.StartKeepAlive(conn.poke, conn.done, func() { conn.broken.Store(true) }, sshx.DefaultKeepAliveInterval)
	return conn, nil
}

// resolveAndMint resolves the asset's account and mints the connection token
// that binds them.
//
// It is the part of opening a connection that needs the bastion's API, and
// it is separate from the dial because the token is what the dial spends.
func resolveAndMint(ctx context.Context, cfg Config, ref jmsapi.AssetRef, account string) (jmsapi.AccountRef, connectionToken, error) {
	if cfg.API == nil {
		return jmsapi.AccountRef{}, connectionToken{},
			fmt.Errorf("%w: the bastion's REST API is not configured", ErrTokenUnavailable)
	}
	acct, err := pickAccount(ctx, cfg, ref.ID, account)
	if err != nil {
		return jmsapi.AccountRef{}, connectionToken{}, err
	}
	tok, err := mintConnectionToken(ctx, cfg, ref.ID, acct.ID)
	if err != nil {
		return jmsapi.AccountRef{}, connectionToken{}, err
	}
	return acct, tok, nil
}
