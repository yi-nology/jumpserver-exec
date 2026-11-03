package koko

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/concurrency/syncx"
	"github.com/oekit/network/sshx"

	"github.com/yi-nology/jumpserver-mcp-kit/model/jms"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/reg"
)

// The branch that used to be dead.
//
// When this session already knows the token path is down, a command that
// could not be delivered has to go to the OTHER path: the command was never
// delivered, so that run is its FIRST delivery (not a replay), and this
// session has already proved the other path works.
//
// It used to ask `canDegrade(err)` — which asks whether the error says the
// token SCHEME is unavailable. The error here says "the command was not
// delivered", so the answer was always no: the branch never degraded, and
// the command FAILED instead of running on a terminal this session had
// already used successfully.
//
// The server here rejects every channel, so the exec request never goes
// out — which is the real shape of "the command was not delivered".
func TestACommandGoesToTheOtherPathWhenTheTokenPathIsKnownDown(t *testing.T) {
	f := newFakeSSH(t)
	client, err := sshx.Dial(context.Background(), f.addr(), clientConfig(), sshx.DialOptions{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	calls := 0
	tb := &tokenBackend{
		asset: "node-071",
		cfg:   Config{Host: "10.0.0.1", Port: 2222, Memory: NewBackendMemory(time.Minute)},
		conn: &pooledConn{
			client: client,
			gate:   syncx.NewGate(1),
			done:   make(chan struct{}),
		},
		// This session already fell back once, so the token path is known
		// to be down and the terminal path is known to work.
		degradedToTUI: true,
		fallback: func(time.Duration) (Backend, error) {
			calls++
			return &fakeBackend{out: "from the terminal\n", code: 0}, nil
		},
	}

	out, code, _, err := tb.Exec("hostname", 30*time.Second)
	if err != nil {
		t.Fatalf("the command failed instead of going to the other path: %v", err)
	}
	if calls != 1 {
		t.Errorf("the other path was used %d times, want 1", calls)
	}
	if out != "from the terminal\n" {
		t.Errorf("the answer says %q, want the terminal's output", out)
	}
	if code != 0 {
		t.Errorf("the exit code is %d, want 0", code)
	}
}

// The delivery state of a command that failed on BOTH paths: nothing was
// sent on either one, and the trail has to say so — "nobody said" would be
// wrong, because we DO know: the command never left this process.
func TestAFailureOnBothPathsSaysNothingWasSent(t *testing.T) {
	f := newFakeSSH(t)
	client, err := sshx.Dial(context.Background(), f.addr(), clientConfig(), sshx.DialOptions{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	tb := &tokenBackend{
		asset: "node-071",
		cfg:   Config{Host: "10.0.0.1", Port: 2222, Memory: NewBackendMemory(time.Minute)},
		conn: &pooledConn{
			client: client,
			gate:   syncx.NewGate(1),
			done:   make(chan struct{}),
		},
		degradedToTUI: true,
		fallback: func(time.Duration) (Backend, error) {
			return nil, ErrNoAccountOnAsset
		},
	}

	_, _, _, err = tb.Exec("hostname", 30*time.Second)
	if err == nil {
		t.Fatal("the command reported success although both paths failed")
	}
	if got := reg.DeliveryOf(err); got != jms.Delivery_not_sent {
		t.Errorf("the delivery state is %v, want not_sent", got)
	}
}
