package policy

// Memory remembers dangerous-command approvals for a short window, the
// kael Approval idea narrowed to one process: an approval granted with
// confirm=true is reusable for the SAME command on the SAME asset until
// it expires. It never stores the command text — the key is a hash, the
// command can carry secrets.
import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

type Memory struct {
	mu   sync.Mutex
	ttl  time.Duration
	hits map[string]time.Time // key → expiry
	// now is the clock the expiry judgement reads. It defaults to
	// time.Now; tests replace it with a hand-wound one so the expiry case
	// needs no real sleeping (a Sleep-based expiry test is a flake waiting
	// for a loaded CI box).
	now func() time.Time
}

func NewMemory(ttl time.Duration) *Memory {
	if ttl < 0 {
		ttl = 0
	}
	return &Memory{ttl: ttl, hits: make(map[string]time.Time), now: time.Now}
}

func (m *Memory) Enabled() bool      { return m.ttl > 0 }
func (m *Memory) TTL() time.Duration { return m.ttl }

func Key(asset, command string) string {
	sum := sha256.Sum256([]byte(asset + "\x00" + strings.TrimSpace(command)))
	return hex.EncodeToString(sum[:])
}

func (m *Memory) Grant(asset, command string) {
	if !m.Enabled() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hits[Key(asset, command)] = m.now().Add(m.ttl)
}

// Satisfied reports a live remembered approval and when it was granted.
func (m *Memory) Satisfied(asset, command string) (bool, time.Time) {
	if !m.Enabled() {
		return false, time.Time{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.hits[Key(asset, command)]
	if !ok || m.now().After(exp) {
		return false, time.Time{}
	}
	return true, exp.Add(-m.ttl)
}
