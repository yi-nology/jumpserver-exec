package koko

// exec_timeout_race_test.go 钉住「超时 + 并发」的协程形状（GitHub #15）。
//
// tokenBackend.run 的超时路径把 sess.Wait() 放进一个 GoSafe 协程，命令
// 超时后靠 sess.Close() 解开它——Wait 协程是**有界的**（每个进行中的
// 命令至多一个，超时或完成即退出），但这一直没有测试覆盖：-race 全绿
// 只说明没有数据竞争，不说明协程数会回落。jmsx_exec_multi 默认并发 4
// 且池 TTL 2m，最坏情况是 N 台机器同时超时——本测试就造这个形状：
// 8 条独立连接同时对一条永不结束的命令超时，断言每条都拿到
// ErrCommandTimeout，且协程数在全部收场后回到基线。
//
// 对端是一个「挂起」的假 SSH 服务端：完成握手、接受 session 通道、对
// exec 回 ok，然后**永不发 exit-status**——真机上命令还在跑的形状。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/concurrency/syncx"
	"github.com/oekit/network/sshx"
	"golang.org/x/crypto/ssh"
)

// hangingSSH is an SSH server whose commands NEVER finish: the handshake
// completes, session channels are accepted, exec is acknowledged, and the
// exit status never comes — the client's sess.Wait() blocks until it is
// closed from its own side.
type hangingSSH struct {
	ln net.Listener

	mu    sync.Mutex
	conns []net.Conn
}

func newHangingSSH(t *testing.T) *hangingSSH {
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
	h := &hangingSSH{ln: ln}
	go h.serve(signer)
	t.Cleanup(func() {
		_ = ln.Close()
		h.dropConnections()
	})
	return h
}

func (h *hangingSSH) addr() string { return h.ln.Addr().String() }

func (h *hangingSSH) serve(signer ssh.Signer) {
	for {
		conn, err := h.ln.Accept()
		if err != nil {
			return
		}
		h.mu.Lock()
		h.conns = append(h.conns, conn)
		h.mu.Unlock()
		go h.handle(conn, signer)
	}
}

func (h *hangingSSH) handle(conn net.Conn, signer ssh.Signer) {
	defer conn.Close()
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "no such channel")
			continue
		}
		accepted, chReqs, err := ch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer accepted.Close()
			for req := range chReqs {
				if req.Type == "exec" {
					// 命令「已交付」——然后永远没有退出码。
					_ = req.Reply(true, nil)
					continue
				}
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
			}
		}()
	}
}

func (h *hangingSSH) dropConnections() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.conns {
		_ = c.Close()
	}
	h.conns = nil
}

// hangBackend 手搭一个 tokenBackend：真 SSH 客户端对 hangingSSH，门与
// broken 标记与线上同一套——测的就是 run() 本身，不是它的替身。
func hangBackend(t *testing.T, h *hangingSSH, i int) *tokenBackend {
	t.Helper()
	client, err := sshx.Dial(context.Background(), h.addr(), clientConfig(), sshx.DialOptions{})
	if err != nil {
		t.Fatalf("dial %d: %v", i, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &tokenBackend{
		conn:  &pooledConn{client: client, gate: syncx.NewGate(1)},
		asset: fmt.Sprintf("hang-%02d", i),
	}
}

// goroutineCount 让调度器先落定再数，避免把「还没排上队」当成泄漏。
func goroutineCount() int {
	runtime.Gosched()
	return runtime.NumGoroutine()
}

func TestConcurrentCommandTimeoutsLeaveNoGoroutinesBehind(t *testing.T) {
	const sessions = 8
	h := newHangingSSH(t)

	backends := make([]*tokenBackend, sessions)
	for i := range backends {
		backends[i] = hangBackend(t, h, i)
	}

	// 基线在全部连接建立后量：服务端与客户端的常驻协程（mux 循环等）
	// 都已在数里，往下只该看见超时协程的涨与落。
	base := goroutineCount()

	var wg sync.WaitGroup
	errs := make([]error, sessions)
	var peak atomic.Int64
	peak.Store(int64(base))
	stopPolling := make(chan struct{})
	// 采样峰值：命令挂起的窗口里，每条连接挂着一个 Wait 协程。
	// （峰值读写在两个协程之间，得走 atomic——这条 race 是测试自己的，
	// 不是被测代码的。）
	go func() {
		for {
			select {
			case <-stopPolling:
				return
			default:
				if n := int64(runtime.NumGoroutine()); n > peak.Load() {
					peak.Store(n)
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()

	start := make(chan struct{})
	for i := range backends {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// 同一条永不结束的命令；500ms 保证 8 条的挂起窗口彼此重叠。
			_, err := backends[i].run("sleep 9999", nil, io.Discard, io.Discard, 500*time.Millisecond)
			errs[i] = err
		}(i)
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		close(stopPolling)
		t.Fatal("the timed-out commands never returned")
	}
	close(stopPolling)

	for i, err := range errs {
		if !errors.Is(err, ErrCommandTimeout) {
			t.Errorf("session %d: err = %v, want ErrCommandTimeout", i, err)
		}
	}
	if gotPeak := peak.Load(); gotPeak <= int64(base) {
		t.Errorf("goroutine peak = %d, base = %d — the wait goroutines never showed up, the shape being tested did not happen", gotPeak, base)
	}

	// 回落：超时协程每个都该在 run 返回前收场（sess.Close() 解开 Wait），
	// 收场之后协程数回到基线。给调度器留一点余量与时间。
	deadline := time.Now().Add(5 * time.Second)
	for goroutineCount() > base+2 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines did not fall back: base = %d, now = %d — a timed-out session left a waiter behind", base, goroutineCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
