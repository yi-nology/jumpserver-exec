package koko

import (
	"testing"
	"time"
)

// 重试分类必须是「类型」判断（network#116 收编）：默认 Retryable 只认握手
// 超时哨兵；认证拒绝是判定不是抖动，必须漏网。dialPolicyOf 把 Retryable 留
// nil 就是在守这条——上游默认只重拨 ErrHandshakeTimeout，措辞匹配的旧教训
//（拒绝文案里也有 "handshake"，曾把拒绝当抖动静默重拨）在 sshx 侧有测试钉。
func TestDialPolicyRetriesOnlyHandshakeTimeouts(t *testing.T) {
	p := dialPolicyOf(Config{DialRetries: 1}) // 1 是 confload 的默认值（JMS_KOKO_DIAL_RETRIES）
	if p.Retryable != nil {
		t.Error("Retryable must stay nil: the sshx default retries ONLY handshake timeouts")
	}
	if p.Retries != 1 {
		t.Errorf("retries = %d, want 1 (one redial of a handshake blip)", p.Retries)
	}
	if got := dialPolicyOf(Config{DialRetries: 3}).Retries; got != 3 {
		t.Errorf("DialRetries=3 → policy retries %d, want 3", got)
	}
}

// 0（或负值）= 关掉重拨：必须旁路 DialWithPolicy 走裸 Dial —— 上游把
// Retries<=0 提升为默认 1，0 的「不重拨」语义若交出去就被吃掉。
func TestRedialOffBypassesThePolicy(t *testing.T) {
	if !redialOff(Config{DialRetries: 0}) {
		t.Error("DialRetries=0 must mean NO redial")
	}
	if !redialOff(Config{DialRetries: -2}) {
		t.Error("a negative DialRetries must also mean NO redial")
	}
	if redialOff(Config{DialRetries: 1}) {
		t.Error("DialRetries=1 is the default redial-once, not off")
	}
}

// 握手界默认 15s（tailscale 实测 5s 内 1/4 会话开超时），配置值优先。
func TestHandshakeTimeoutOf(t *testing.T) {
	if got := handshakeTimeoutOf(Config{}); got != 15*time.Second {
		t.Errorf("default handshake timeout = %v, want 15s", got)
	}
	if got := handshakeTimeoutOf(Config{HandshakeTimeout: 5 * time.Second}); got != 5*time.Second {
		t.Errorf("configured value must win, got %v", got)
	}
}
