package koko

// The pool's bookkeeping, tested without a bastion.
//
// The parts of the pool with locks in them are exactly the parts a bastion
// cannot be asked about: who wins a race for the same asset, whether a
// connection that is in use survives an eviction, whether the reaper touches
// a busy connection.
//
// These tests drive the bookkeeping directly — the dialer is injected — so
// they can hammer it from many goroutines at once. Run them with -race:
// a lock that is wrong here is a lock that is wrong in production too.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/concurrency/syncx"

	"github.com/yi-nology/jumpserver-mcp-kit/pkg/jmsapi"
)

// fakeAPI returns a client whose bastion answers with a fixed set of
// assets. The id of an asset is "id-" plus its name, so a test can tell two
// machines apart by their spelling.
func fakeAPI(t *testing.T) *jmsapi.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The listing of assets, and the detail of one (which carries its
		// accounts). The path tells them apart.
		if strings.HasSuffix(r.URL.Path, "/") && r.URL.Path == "/api/v1/assets/assets/" {
			_, _ = fmt.Fprint(w, `{"count":3,"results":[`+
				`{"id":"id-host-1","name":"host-1","address":"10.0.0.1"},`+
				`{"id":"id-host-a","name":"host-a","address":"10.0.0.2"},`+
				`{"id":"id-host-b","name":"host-b","address":"10.0.0.3"}]}`)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/assets/assets/"), "/")
		_, _ = fmt.Fprintf(w, `{"id":%q,"name":%q,"accounts":[`+
			`{"id":"acc-1","name":"opsuser","username":"opsuser","is_active":true}]}`,
			id, strings.TrimPrefix(id, "id-"))
	}))
	t.Cleanup(srv.Close)
	return jmsapi.New(jmsapi.Options{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()})
}

// fakePool returns a pool whose dialer counts its calls and hands back a
// connection with no client, so nothing touches the network.
func fakePool(ttl time.Duration, dials *int32) *Pool {
	p := NewPool(Config{}, ttl)
	p.dial = func(ctx context.Context, cfg Config, asset, account string, timeout time.Duration) (*pooledConn, error) {
		atomic.AddInt32(dials, 1)
		return &pooledConn{assetName: asset, account: account, gate: syncx.NewGate(1)}, nil
	}
	return p
}

// The pool must open ONE connection for an asset, however many callers ask
// for it at the same time. Two connections to the same machine means two
// sshd session processes on it, and the second one buys nothing.
func TestThePoolOpensOneConnectionPerAsset(t *testing.T) {
	var dials int32
	p := fakePool(time.Minute, &dials)

	const callers = 16
	var wg sync.WaitGroup
	got := make([]*pooledConn, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, _, err := p.get(context.Background(), Config{API: fakeAPI(t)}, "host-1", "", time.Second)
			if err != nil {
				t.Errorf("get: %v", err)
				return
			}
			got[i] = c
		}(i)
	}
	wg.Wait()

	if n := atomic.LoadInt32(&dials); n != 1 {
		t.Errorf("the pool dialled %d times, want 1", n)
	}
	for i := 1; i < callers; i++ {
		if got[i] != got[0] {
			t.Fatal("two callers got different connections for the same asset")
		}
	}
	if p.Size() != 1 {
		t.Errorf("the pool holds %d connections, want 1", p.Size())
	}
}

// A connection that is in use must survive an eviction. Closing the client
// under a running command would kill that command half-way — and a command
// that was delivered may not be replayed.
func TestEvictWaitsForAConnectionThatIsInUse(t *testing.T) {
	var dials int32
	p := fakePool(time.Minute, &dials)

	c, _, err := p.get(context.Background(), Config{API: fakeAPI(t)}, "host-1", "", time.Second)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// Hold the connection the way a running command would.
	release, err := c.gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire the connection: %v", err)
	}
	closed := make(chan struct{})
	go func() {
		p.evict(c)
		close(closed)
	}()

	select {
	case <-closed:
		t.Error("the eviction finished while the connection was in use")
	case <-time.After(150 * time.Millisecond):
		// Good: the eviction is waiting for the command to finish.
	}
	release()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Error("the eviction did not finish after the connection was released")
	}
}

// The reaper must not close a connection that a command is using. A command
// that runs longer than the idle TTL would otherwise be killed by it.
func TestTheReaperLeavesBusyConnectionsAlone(t *testing.T) {
	var dials int32
	p := fakePool(20*time.Millisecond, &dials)

	c, _, err := p.get(context.Background(), Config{API: fakeAPI(t)}, "host-1", "", time.Second)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	release, err := c.gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire the connection: %v", err)
	}
	defer release()

	// Age the connection past the TTL, as if it had been idle — while a
	// command holds it.
	c.lastUse = time.Now().Add(-time.Second)
	p.reap()

	if p.Size() != 1 {
		t.Errorf("the reaper collected a connection that was in use (%d left)", p.Size())
	}
}

// The idle TTL is the reaper's business, and a connection nobody is using
// must actually go.
func TestTheReaperCollectsIdleConnections(t *testing.T) {
	var dials int32
	p := fakePool(20*time.Millisecond, &dials)

	if _, _, err := p.get(context.Background(), Config{API: fakeAPI(t)}, "host-1", "", time.Second); err != nil {
		t.Fatalf("get: %v", err)
	}
	p.mu.Lock()
	for _, c := range p.conns {
		c.lastUse = time.Now().Add(-time.Second)
	}
	p.mu.Unlock()

	p.reap()
	if p.Size() != 0 {
		t.Errorf("the pool holds %d connections, want 0 after the reaper", p.Size())
	}
}

// Releasing a connection hands it back; it does not close it. That is the
// whole point of the pool.
func TestReleaseKeepsTheConnection(t *testing.T) {
	var dials int32
	p := fakePool(time.Minute, &dials)

	c, _, err := p.get(context.Background(), Config{API: fakeAPI(t)}, "host-1", "", time.Second)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	p.release(c)
	if p.Size() != 1 {
		t.Errorf("the pool holds %d connections after a release, want 1", p.Size())
	}
	// And the next caller gets the same one, without a second dial.
	again, reused, err := p.get(context.Background(), Config{API: fakeAPI(t)}, "host-1", "", time.Second)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !reused {
		t.Error("the second get did not reuse the connection")
	}
	if again != c {
		t.Error("the second get handed out a different connection")
	}
	if n := atomic.LoadInt32(&dials); n != 1 {
		t.Errorf("the pool dialled %d times, want 1", n)
	}
}

// The entries the status tool reports must describe what the pool holds.
func TestEntriesDescribeThePool(t *testing.T) {
	var dials int32
	p := fakePool(time.Minute, &dials)
	for _, a := range []string{"host-b", "host-a"} {
		if _, _, err := p.get(context.Background(), Config{API: fakeAPI(t)}, a, "opsuser", time.Second); err != nil {
			t.Fatalf("get: %v", err)
		}
	}
	entries := p.Entries()
	if len(entries) != 2 {
		t.Fatalf("the pool reports %d entries, want 2", len(entries))
	}
	// Sorted by name, so the report is stable.
	if entries[0].Asset != "host-a" || entries[1].Asset != "host-b" {
		t.Errorf("the entries are not sorted by name: %q, %q", entries[0].Asset, entries[1].Asset)
	}
	for _, e := range entries {
		if e.Account != "opsuser" {
			t.Errorf("the entry for %s says account %q", e.Asset, e.Account)
		}
	}
}

// Two spellings of the same machine are one machine, and they must share
// one connection — otherwise the machine carries two sshd sessions and the
// second buys nothing.
func TestTheKeyIsTheResolvedAssetNotTheSpelling(t *testing.T) {
	if poolKey("asset-uuid", "acct") != poolKey("asset-uuid", "acct") {
		t.Fatal("the key is not stable")
	}
	if poolKey("asset-uuid", "acct") == poolKey("asset-uuid", "other") {
		t.Error("two accounts share a key")
	}
	// The spelling never enters the key: the caller resolves first.
	if got := poolKey("42e1a002-e33f-4c38-ad16-479cadb2df41", "opsuser"); got !=
		"42e1a002-e33f-4c38-ad16-479cadb2df41|opsuser" {
		t.Errorf("unexpected key %q", got)
	}
}
