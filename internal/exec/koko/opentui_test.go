package koko

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ⚠️ An unset host must be refused BEFORE anything is dialled.
//
// The address would be ":2222", and Go reads an empty host as THIS machine
// ⇒ the terminal fallback would hand the koko private key to whatever
// listens on this box's 2222 — a machine that is not the bastion. The host
// has no default on purpose (a default would point one deployment's
// clients at somebody else's bastion), so an unset one has to be an error
// rather than a localhost.
func TestOpenTUIRefusesToDialWithoutAHost(t *testing.T) {
	_, err := OpenTUI(context.Background(), Config{Port: 2222, User: "ai"}, "some-box", "", 5*time.Second)
	if err == nil {
		t.Fatal("OpenTUI dialled with no host configured")
	}
	if !strings.Contains(err.Error(), "JMS_BASTION_HOST") {
		t.Errorf("the error does not name the variable to set: %v", err)
	}
	// The message must not look like a network failure — the whole point is
	// that nothing was dialled.
	if strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "dial tcp") {
		t.Errorf("the error reads like a network failure: %v", err)
	}
}
