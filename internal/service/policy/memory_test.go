package policy

import (
	"strings"
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
	m.Grant("a", "cmd")
	time.Sleep(80 * time.Millisecond)
	if ok, _ := m.Satisfied("a", "cmd"); ok {
		t.Fatal("expired approval still satisfied")
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
