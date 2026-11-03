package koko

// keepalive_wire_test.go pins the request that actually goes out on the wire.
//
// The upstream primitive (oekit/network/sshx) drives its loop with a stub
// function, so the request NAME is not covered by any test up there. If this
// side sends the wrong name, no test fails anywhere: the keepalive silently
// stops working, the pooled connection is cut by an idle timeout, and the
// next call finds a dead socket instead of a working one.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oekit/concurrency/syncx"
	"github.com/oekit/network/sshx"
)

func TestTheKeepaliveRequestGoesOutOnTheWire(t *testing.T) {
	f := newFakeSSH(t)
	client, err := sshx.Dial(context.Background(), f.addr(), clientConfig(), sshx.DialOptions{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	conn := &pooledConn{
		client: client,
		gate:   syncx.NewGate(1),
		done:   make(chan struct{}),
	}
	if err := conn.poke(); err != nil {
		t.Fatalf("poke: %v", err)
	}

	select {
	case got := <-f.requests:
		if got != sshx.KeepAliveRequest {
			t.Errorf("线上收到的请求 = %q，期望 %q", got, sshx.KeepAliveRequest)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("线上没有收到任何全局请求")
	}
}

// A busy connection is not poked at all -- it is not idle, so it does not
// need warming, and the command running on it is keeping it warm by itself.
func TestTheKeepaliveSkipsABusyConnection(t *testing.T) {
	f := newFakeSSH(t)
	client, err := sshx.Dial(context.Background(), f.addr(), clientConfig(), sshx.DialOptions{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	conn := &pooledConn{
		client: client,
		gate:   syncx.NewGate(1),
		done:   make(chan struct{}),
	}
	// Take the slot: the connection is now busy.
	release, ok := conn.gate.TryAcquire()
	if !ok {
		t.Fatal("could not take the gate")
	}
	err = conn.poke()
	release()
	if err == nil {
		t.Fatal("a busy connection must not be poked")
	}
	// The busy sentinel is the one the keepalive loop recognises -- a
	// different value would be read as "the connection is gone".
	if !errors.Is(err, sshx.ErrBusy) {
		t.Errorf("忙碌信号 = %v，期望 %v", err, sshx.ErrBusy)
	}
	select {
	case got := <-f.requests:
		t.Errorf("忙着的连接不该发出请求，却收到了 %q", got)
	case <-time.After(200 * time.Millisecond):
	}
}
