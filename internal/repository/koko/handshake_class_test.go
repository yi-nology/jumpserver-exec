package koko

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/oekit/network/sshx"
)

// 握手失败的重试分类必须是「类型」判断，不是措辞判断（network#116 收编）。
// 旧的字符串匹配把「握手被拒绝」（认证拒绝）也当成抖动：错凭据被静默重拨、
// 判定被盖掉。新判据只认超时哨兵；拒绝必须漏网（不重拨）。
func TestHandshakeRetryClassifiesByTypeNotWording(t *testing.T) {
	timeout := &sshx.HandshakeTimeoutError{Addr: "b:2222", Bound: 15 * time.Second,
		Err: errors.New("EOF")}
	if !isHandshakeFailure(timeout) {
		t.Error("a handshake timeout is a blip and must retry")
	}
	if !isHandshakeFailure(fmt.Errorf("dial: %w", timeout)) {
		t.Error("the sentinel must be found through the wrap chain")
	}

	rejected := &sshx.HandshakeRejectedError{Addr: "b:2222",
		Err: errors.New("unable to authenticate")}
	if isHandshakeFailure(rejected) {
		t.Error("an auth refusal is a verdict and must NOT retry")
	}
	// 措辞里有 "handshake" 的旧式裸错误不再触发重拨：分类走类型不走措辞。
	if isHandshakeFailure(errors.New("ssh: handshake failed: something else")) {
		t.Error("wording alone must not classify; the old matcher did")
	}
}
