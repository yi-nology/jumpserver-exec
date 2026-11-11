package koko

// 平台感知的会话门（exec#29 / #31，2026-11-11 对 v5 堡垒机实测的假成功）。
//
// K8s 平台的资产开不出 shell：连接令牌能签（协议 ssh/k8s 都回 201）、koko
// 也认令牌做 SSH 登录，但 koko 拒 exec 通道——不拦的话命令类工具拿到的是
// 「exit 0 + 空输出」的假成功，agent 会误读成「机器上没有这个输出」。
// PTY 兜底同样不可依赖（管子通但同参数行为漂移：有时打印集群报错，有时
// 静默断）。所以在会话开起来之前按平台明确拒绝，指引跳板机 kubectl 路径
// （配方：skills/jms-exec-ops/references/k8s-kubectl.md，链路实测在案）。
//
// 门的位置在 resolveAsset 的三个调用点（own 连接、池连接、TUI 前检）——
// 都在「任何东西发出去之前」，失败一律 not_sent（重试安全：重试还是被拒）。

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yi-nology/jumpserver-kit/pkg/jmsapi"
)

// ErrK8sAssetNoShell says the asset's platform cannot host a shell session.
//
// It is its own error because it decides something: the TUI fallback must
// NOT take over for it (the terminal would fail unpredictably on the same
// asset — that is the behaviour-drift the measurement showed), so it must
// be distinguishable from ErrTokenUnavailable (which is exactly the case
// the fallback exists for).
var ErrK8sAssetNoShell = errors.New("kubernetes asset has no shell")

// isK8sPlatform reports whether a platform name is a Kubernetes one.
//
// The name is the bastion's display name ("Kubernetes" on the measured
// deployment); the match is case-insensitive and covers the k8s shorthand
// and suffixed names ("k8s-prod"). An empty name (an asset whose platform
// did not resolve) is not a match — the gate refuses on EVIDENCE, not on
// the absence of it.
func isK8sPlatform(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(n, "kubernetes") || strings.Contains(n, "k8s")
}

// refuseShelllessPlatform refuses a session on an asset whose platform
// cannot host a shell.
//
// The refusal carries the redirection the agent needs: the jump-host kubectl
// path is the measured alternative (exit 0, full output, audit trail through
// the bastion), and pointing at the recipe keeps the "how" one click away.
func refuseShelllessPlatform(ref jmsapi.AssetRef) error {
	if !isK8sPlatform(ref.Platform) {
		return nil
	}
	return fmt.Errorf("%w: asset %q is on platform %q — koko refuses the exec channel "+
		"on kubernetes assets, so a command session would answer exit 0 with empty output "+
		"(a fake success). Use the jump-host kubectl path instead: run kubectl on a managed "+
		"machine that has it, via jmsx_exec on that machine's SSH asset — the session audit "+
		"trail still goes through the bastion (recipe: skills/jms-exec-ops/references/k8s-kubectl.md)",
		ErrK8sAssetNoShell, ref.Name, ref.Platform)
}
