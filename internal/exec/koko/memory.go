package koko

// memory.go remembers which backend worked.
//
// Why it exists. The connection-token path is the fast one, and the terminal
// path is the fallback. When the bastion does not offer the token scheme at
// all — the endpoint is missing, the deployment refuses token logins, the
// API is not reachable — every single call pays for the failed attempt
// before the fallback takes over. A sweep of fifty hosts pays it fifty
// times, and each of those attempts is a round trip to the bastion that is
// known to be useless.
//
// What it remembers is a fact about the BASTION, not about one asset: the
// token is minted fresh for every connection, so a koko that refuses token
// logins is refusing them for everything. An asset that cannot be logged
// into is a different thing, and it is caught BEFORE the backend is chosen
// (no usable account on the asset) — so it never reaches this memory.
//
// The memory is IN-PROCESS, and that is deliberate. It is shared by every
// call of one server, which is where the cost is (one failed attempt per
// sweep instead of one per host), and it costs nothing to keep honest. A
// file would carry the same fact across processes, at the price of staleness
// and of a second thing that can be wrong.
//
// The memory EXPIRES, and that is the part that keeps it honest: a backend
// that starts working again has to be found again, and the only way to find
// it is to try it. So once the TTL is up, the preferred backend is tried
// first again, whatever the memory said. The memory accelerates the cold
// open; it never hides a recovery.

import (
	"sort"
	"sync"
	"time"
)

// DefaultBackendMemoryTTL is how long a remembered backend is trusted.
//
// Ten minutes: long enough that a sweep does not re-probe per host, short
// enough that a bastion whose token scheme came back is found again in the
// same afternoon.
const DefaultBackendMemoryTTL = 10 * time.Minute

// BackendMemory remembers the backend that worked, per bastion.
//
// The key is the koko address, because that is what a session is opened
// against. (The thing that usually fails is the REST side of the bastion —
// the connection-token endpoint — but the two are configured together and
// point at the same deployment.)
//
// It is safe for concurrent use: the sessions of a fleet sweep all read it
// at once.
type BackendMemory struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
	ttl     time.Duration
}

// memoryEntry is what is remembered about one bastion.
type memoryEntry struct {
	backend string
	at      time.Time
}

// NewBackendMemory returns a memory with the given TTL.
//
// A TTL of zero (or less) DISABLES the memory: it returns nil, and every
// call then uses the default order, and nothing is remembered. (A nil
// memory is safe to use: every method of it tolerates a nil receiver.)
func NewBackendMemory(ttl time.Duration) *BackendMemory {
	if ttl <= 0 {
		return nil
	}
	return &BackendMemory{entries: map[string]memoryEntry{}, ttl: ttl}
}

// Preferred returns the backend that worked last time for this bastion.
//
// It returns "" when there is nothing usable to say: no memory, an expired
// memory, or a disabled one. The caller then uses the default order — which
// is what re-probes a backend that came back.
func (m *BackendMemory) Preferred(bastion string) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[bastion]
	if !ok {
		return ""
	}
	if time.Since(e.at) > m.ttl {
		return ""
	}
	return e.backend
}

// Remember records that a backend worked for this bastion.
//
// The timestamp is NOT refreshed when the same backend works again, and
// that is the whole point: the memory has to EXPIRE while the remembered
// backend keeps working. Otherwise the other path is never tried, its
// recovery is hidden for as long as the remembered path works, and nothing
// says so.
//
// The timestamp IS refreshed when the backend CHANGES: that is a new
// observation, and the memory starts counting down from it.
//
// An empty backend is ignored: a caller that never observed one cannot
// erase what an earlier call learned.
func (m *BackendMemory) Remember(bastion, backend string) {
	if m == nil || bastion == "" || backend == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[bastion]; ok && e.backend == backend {
		return
	}
	m.entries[bastion] = memoryEntry{backend: backend, at: time.Now()}
}

// Entries reports what is remembered, for the doctor.
//
// It returns the bastion, the backend and how long ago it was recorded.
// The list is sorted, so the doctor does not print a different bastion
// every time it runs.
func (m *BackendMemory) Entries() []MemoryEntry {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MemoryEntry, 0, len(m.entries))
	for bastion, e := range m.entries {
		out = append(out, MemoryEntry{
			Bastion: bastion,
			Backend: e.backend,
			At:      e.at,
			Age:     time.Since(e.at),
			Expired: time.Since(e.at) > m.ttl,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bastion < out[j].Bastion })
	return out
}

// MemoryEntry is one remembered fact.
type MemoryEntry struct {
	// Bastion is the bastion it is about.
	Bastion string
	// Backend is the backend that worked.
	Backend string
	// At is when it was recorded.
	At time.Time
	// Age is how long ago that was.
	Age time.Duration
	// Expired says the memory is too old to trust.
	Expired bool
}
