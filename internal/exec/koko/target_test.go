package koko

import (
	"strings"
	"testing"
)

// An unset host or user has to say so BY NAME.
//
// ⚠️ The host and the user have no defaults (a default would point one
// deployment's clients at somebody else's bastion) ⇒ the dial error used to
// read "dial koko :2222 as :" — a blank that looks like a typo rather than
// "this was never configured", and sends the operator after the network
// instead of after the configuration.
func TestTheDialTargetNamesWhatIsNotConfigured(t *testing.T) {
	got := (Config{Port: 2222}).target()
	for _, want := range []string{"JMS_BASTION_HOST", "JMS_KOKO_USER", "2222"} {
		if !strings.Contains(got, want) {
			t.Errorf("the target %q does not name %s", got, want)
		}
	}
	// ⚠️ A blank must not survive: that is the whole point.
	if strings.Contains(got, ": as ") || strings.Contains(got, ":2222 as :") {
		t.Errorf("the target %q still prints a blank field", got)
	}

	// A configured one reads as before.
	got = (Config{Host: "bastion.example.com", Port: 2222, User: "ai"}).target()
	if got != "bastion.example.com:2222 as ai" {
		t.Errorf("the target = %q, want %q", got, "bastion.example.com:2222 as ai")
	}
}

// The address used for the DIAL is not the one used for the message: the
// dial needs the bare host, and a placeholder would be dialed.
func TestTheDialAddressIsNotTheDescribedTarget(t *testing.T) {
	c := Config{Host: "bastion.example.com", Port: 2222, User: "ai"}
	if c.address() != "bastion.example.com:2222" {
		t.Errorf("address() = %q, want %q", c.address(), "bastion.example.com:2222")
	}
	empty := Config{Port: 2222}
	if empty.address() != ":2222" {
		t.Errorf("address() = %q, want %q (the raw host, placeholders are for messages only)",
			empty.address(), ":2222")
	}
}
