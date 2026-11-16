package policy

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMemoryGrantThenSatisfied(t *testing.T) {
	m := NewMemory(10 * time.Minute)
	if ok, _ := m.Satisfied("node-1", "rm -rf /tmp/x"); ok {
		t.Fatal("satisfied before any grant")
	}
	m.Grant("node-1", "rm -rf /tmp/x")
	ok, at := m.Satisfied("node-1", "rm -rf /tmp/x")
	if !ok || at.IsZero() {
		t.Fatal("grant not remembered")
	}
	if ok, _ := m.Satisfied("node-1", "rm -rf /tmp/y"); ok {
		t.Fatal("different command satisfied")
	}
	if ok, _ := m.Satisfied("node-2", "rm -rf /tmp/x"); ok {
		t.Fatal("different asset satisfied")
	}
}

func TestMemoryExpiry(t *testing.T) {
	m := NewMemory(50 * time.Millisecond)
	// A hand-wound clock, not a Sleep: the expiry judgement reads m.now,
	// so the test winds it past the TTL deterministically — a Sleep-based
	// expiry test is a flake waiting for a loaded CI box.
	now := time.Now()
	m.now = func() time.Time { return now }
	m.Grant("a", "cmd")
	now = now.Add(80 * time.Millisecond)
	if ok, _ := m.Satisfied("a", "cmd"); ok {
		t.Fatal("expired approval still satisfied")
	}
	// And just before the expiry the SAME approval is still live — the
	// boundary is the TTL, not "any later read".
	m2 := NewMemory(50 * time.Millisecond)
	at := time.Now()
	m2.now = func() time.Time { return at }
	m2.Grant("a", "cmd")
	at = at.Add(40 * time.Millisecond)
	if ok, _ := m2.Satisfied("a", "cmd"); !ok {
		t.Fatal("live approval reported expired before the TTL ran out")
	}
}

// Grant and Satisfied run from many goroutines at once (the sweep tools
// reach the memory from per-host workers). Run with -race: the map and the
// clock have to stay consistent under concurrent reads and writes.
func TestMemoryConcurrentGrantAndSatisfied(t *testing.T) {
	m := NewMemory(10 * time.Minute)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			asset := fmt.Sprintf("node-%d", g)
			for i := 0; i < 200; i++ {
				cmd := fmt.Sprintf("rm -rf /tmp/x-%d", i%4)
				m.Grant(asset, cmd)
				m.Satisfied(asset, cmd)
				m.Satisfied(asset, "rm -rf /tmp/other")
			}
		}(g)
	}
	wg.Wait()
	if ok, _ := m.Satisfied("node-0", "rm -rf /tmp/x-0"); !ok {
		t.Fatal("a granted approval was lost under concurrency")
	}
}

func TestMemoryDisabledAtZeroTTL(t *testing.T) {
	m := NewMemory(0)
	m.Grant("a", "cmd") // 不 panic
	if m.Enabled() {
		t.Fatal("zero TTL must be disabled")
	}
	if ok, _ := m.Satisfied("a", "cmd"); ok {
		t.Fatal("disabled memory satisfied")
	}
}

func TestKeyNeverContainsCommandText(t *testing.T) {
	k := Key("node-1", "rm -rf /tmp/secret-xyzzy")
	if strings.Contains(k, "secret") || strings.Contains(k, "rm -rf") {
		t.Fatal("key leaks command text")
	}
	if Key("node-1", "  rm -rf /tmp/secret-xyzzy  ") != k {
		t.Fatal("key must be insensitive to surrounding whitespace")
	}
}
