package koko

import (
	"sync"
	"testing"
	"time"
)

// The memory is what keeps a bastion where the connection-token scheme is
// unavailable from being paid for on every call. These tests pin what it
// does and — more importantly — what it does NOT do.

func TestTheMemoryRemembersTheBackendThatWorked(t *testing.T) {
	m := NewBackendMemory(time.Minute)
	if got := m.Preferred("10.0.0.1:2222"); got != "" {
		t.Errorf("a fresh memory says %q, want nothing", got)
	}
	m.Remember("10.0.0.1:2222", BackendTUI)
	if got := m.Preferred("10.0.0.1:2222"); got != BackendTUI {
		t.Errorf("the memory says %q, want %q", got, BackendTUI)
	}
	// A different bastion has its own memory.
	if got := m.Preferred("10.0.0.2:2222"); got != "" {
		t.Errorf("the memory of one bastion leaked into another: %q", got)
	}
}

// The memory expires, and that is what keeps it honest: a backend that
// started working again has to be found again, and the only way to find it
// is to try it.
func TestTheMemoryExpires(t *testing.T) {
	m := NewBackendMemory(20 * time.Millisecond)
	m.Remember("10.0.0.1:2222", BackendTUI)
	if got := m.Preferred("10.0.0.1:2222"); got != BackendTUI {
		t.Fatalf("the memory says %q, want %q", got, BackendTUI)
	}
	time.Sleep(40 * time.Millisecond)
	if got := m.Preferred("10.0.0.1:2222"); got != "" {
		t.Errorf("an expired memory still says %q", got)
	}
}

// An empty backend is not a fact: a caller that never observed one cannot
// erase what an earlier call learned.
func TestTheMemoryIgnoresAnEmptyBackend(t *testing.T) {
	m := NewBackendMemory(time.Minute)
	m.Remember("10.0.0.1:2222", BackendTUI)
	m.Remember("10.0.0.1:2222", "")
	if got := m.Preferred("10.0.0.1:2222"); got != BackendTUI {
		t.Errorf("the memory says %q, want %q", got, BackendTUI)
	}
}

// A memory of zero TTL is a memory that is OFF: the configuration says
// "set 0 to turn it off", and the code has to agree with the sentence.
func TestAZeroTTLDisablesTheMemory(t *testing.T) {
	if m := NewBackendMemory(0); m != nil {
		t.Errorf("a zero TTL gave a memory, want none")
	}
	if m := NewBackendMemory(-time.Second); m != nil {
		t.Errorf("a negative TTL gave a memory, want none")
	}
}

// The memory has to EXPIRE while the remembered backend keeps working —
// otherwise the other path is never tried and its recovery is hidden for as
// long as the remembered one works.
func TestTheMemoryExpiresEvenWhileTheBackendKeepsWorking(t *testing.T) {
	m := NewBackendMemory(30 * time.Millisecond)
	m.Remember("10.0.0.1:2222", BackendTUI)
	// The same backend works again, over and over.
	for i := 0; i < 5; i++ {
		time.Sleep(10 * time.Millisecond)
		m.Remember("10.0.0.1:2222", BackendTUI)
	}
	if got := m.Preferred("10.0.0.1:2222"); got != "" {
		t.Errorf("the memory still says %q; a working backend must not keep it alive forever", got)
	}
}

// The sessions of a sweep all read the memory at once.
func TestTheMemoryIsSafeForConcurrentUse(t *testing.T) {
	m := NewBackendMemory(time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				m.Remember("10.0.0.1:2222", BackendTUI)
				_ = m.Preferred("10.0.0.1:2222")
				_ = m.Entries()
			}
		}(i)
	}
	wg.Wait()
}

// The order the backends are tried in is the ONLY thing the memory changes.
func TestTheMemoryMovesTheTerminalPathToTheFront(t *testing.T) {
	cfg := Config{Host: "10.0.0.1", Port: 2222, Memory: NewBackendMemory(time.Minute)}
	if got := backendOrder(cfg); got[0] != BackendToken {
		t.Errorf("the default order starts with %q, want %q", got[0], BackendToken)
	}
	cfg.Memory.Remember(cfg.address(), BackendTUI)
	if got := backendOrder(cfg); got[0] != BackendTUI {
		t.Errorf("the remembered order starts with %q, want %q", got[0], BackendTUI)
	}
	// The other backend is still in the order: the memory reorders, it
	// never skips.
	if len(backendOrder(cfg)) != 2 {
		t.Errorf("the order has %d backends, want 2", len(backendOrder(cfg)))
	}
}

// A session that could not be reopened may be retried on the other path
// ONLY when the fast path itself stopped working. An asset that cannot be
// logged into is not a reason to walk the terminal: it would fail there
// too, ten seconds later.
func TestOnlyAMechanismFailureMayDegrade(t *testing.T) {
	if !canDegrade(ErrTokenUnavailable) {
		t.Error("an unavailable token scheme may not degrade to the terminal path")
	}
	if canDegrade(ErrNoAccountOnAsset) {
		t.Error("an asset with no account may not degrade to the terminal path")
	}
	// ⚠️ "The command was not delivered" is NOT a reason to refuse
	// degrading: it says the opposite — the command never reached the
	// machine, so running it on the other path is its FIRST delivery,
	// not a replay. What `canDegrade` still guards is the QUESTION it
	// answers ("is the token SCHEME unavailable?"), and a broken
	// connection does not answer it — which is why the code that degrades
	// asks about the token path being down, not about this error.
	//
	// (The reason this line used to give — "the command might have been
	// delivered" — was wrong: `errNotDelivered` says the opposite.)
	if canDegrade(errNotDelivered) {
		t.Error("a broken connection does not say the token scheme is unavailable")
	}
}
