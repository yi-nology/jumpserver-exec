// fakessh_test.go 是测试用的假 SSH 服务端。
//
// 拨号与保活这两个能力的实现已经下沉到 oekit/network/sshx（连同它们的界限
// 测试一起），本仓这边只留下「需要一个会完成握手的对端」这个夹具——本仓
// 自己的降级分支测试还要用它。

package koko

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakeSSH is a minimal SSH server: it completes the handshake, answers
// global requests, and counts them.
//
// It exists so that the transport's bounds are tested against a real SSH
// conversation rather than a mock — the thing being bounded IS the
// conversation, so a mock would only prove that the mock is bounded.
type fakeSSH struct {
	ln       net.Listener
	requests chan string

	mu    sync.Mutex
	conns []net.Conn
}

// newFakeSSH starts a server on a random port.
func newFakeSSH(t *testing.T) *fakeSSH {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate a host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("sign the host key: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeSSH{ln: ln, requests: make(chan string, 64)}
	go f.serve(signer)
	t.Cleanup(func() {
		_ = ln.Close()
		f.dropConnections()
	})
	return f
}

// addr is where the server listens.
func (f *fakeSSH) addr() string { return f.ln.Addr().String() }

// serve accepts connections until the listener is closed.
func (f *fakeSSH) serve(signer ssh.Signer) {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn, signer)
	}
}

// handle runs one connection: the handshake, and then the global requests.
func (f *fakeSSH) handle(conn net.Conn, signer ssh.Signer) {
	f.mu.Lock()
	f.conns = append(f.conns, conn)
	f.mu.Unlock()
	defer conn.Close()

	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go func() {
		for req := range reqs {
			select {
			case f.requests <- req.Type:
			default:
			}
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		}
	}()
	for ch := range chans {
		_ = ch.Reject(ssh.Prohibited, "this server has no channels")
	}
}

// dropConnections kills every connection the server holds.
func (f *fakeSSH) dropConnections() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		_ = c.Close()
	}
	f.conns = nil
}

// clientConfig is a client configuration the fake server accepts.
func clientConfig() *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            "JMS-token",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
}
