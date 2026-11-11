package koko

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/jumpserver-kit/pkg/jmsapi"
)

// k8sFakeAPI serves a bastion listing with one Kubernetes asset and one
// Linux asset, so a test can prove the gate refuses on EVIDENCE (the
// platform field) and not on anything else about the asset.
func k8sFakeAPI(t *testing.T) *jmsapi.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"count":2,"results":[`+
			`{"id":"id-studio-prod","name":"studio-prod","address":"10.0.0.9","platform":{"name":"Kubernetes"}},`+
			`{"id":"id-node-031","name":"node-031","address":"10.0.0.31","platform":{"name":"Linux"}}]}`)
	}))
	t.Cleanup(srv.Close)
	return jmsapi.New(jmsapi.Options{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()})
}

// K8s 平台资产的假成功（exec#29，2026-11-11 v5 实测）：连接令牌签得出、
// koko 认令牌登录，但 koko 拒 exec 通道——不拦的话命令工具拿到
// 「exit 0 + 空输出」。门在令牌签发之前：解析出平台就拒，给出跳板机
// kubectl 的指引。
//
// ⚠️ 拒绝不是 ErrTokenUnavailable：终端兜底对同一台资产只会漂移得更难读
// （PTY 行为漂移，同实测），兜底不得接管。
func TestK8sAssetsAreRefusedBeforeASessionOpens(t *testing.T) {
	cfg := Config{API: k8sFakeAPI(t)}

	_, err := openToken(context.Background(), cfg, "studio-prod", "", time.Second)
	if err == nil {
		t.Fatal("a kubernetes asset must not open a command session")
	}
	if !errors.Is(err, ErrK8sAssetNoShell) {
		t.Fatalf("errors.Is(err, ErrK8sAssetNoShell) = false (err: %v)", err)
	}
	if errors.Is(err, ErrTokenUnavailable) {
		t.Fatal("the platform refusal must not be classified as a token-scheme failure — the terminal fallback would take over")
	}
	for _, want := range []string{"studio-prod", "Kubernetes", "kubectl", "jmsx_exec"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q, got: %v", want, err)
		}
	}

	// 池路径同一个门：resolveAsset 之后的第二处接入点。
	p := NewPool(cfg, time.Minute)
	if _, _, err := p.get(context.Background(), cfg, "studio-prod", "", time.Second); !errors.Is(err, ErrK8sAssetNoShell) {
		t.Fatalf("the pooled path refused with %v, want ErrK8sAssetNoShell", err)
	}

	// TUI 前检同一个门：硬拒绝不许被「bastion 没答上」的软处理吞掉，
	// 否则十秒步行把 PTY 漂移请回来。
	if err := checkAssetUsable(context.Background(), cfg, "studio-prod", ""); !errors.Is(err, ErrK8sAssetNoShell) {
		t.Fatalf("checkAssetUsable refused with %v, want ErrK8sAssetNoShell (it must not be swallowed)", err)
	}
}

// 对照组（负例）：Linux 平台的资产不被平台门拦——门认证据（平台名），
// 不认「不是这台机器的别的特征」。
func TestNonK8sAssetsPassThePlatformGate(t *testing.T) {
	cfg := Config{API: k8sFakeAPI(t)}

	ref, err := resolveAsset(context.Background(), cfg, "node-031")
	if err != nil {
		t.Fatalf("resolveAsset(node-031) = %v, want a clean resolution", err)
	}
	if ref.Platform != "Linux" {
		t.Fatalf("platform = %q, want Linux", ref.Platform)
	}
	if err := refuseShelllessPlatform(ref); err != nil {
		t.Fatalf("a Linux asset must pass the gate, got: %v", err)
	}
}

// 平台名的判据：大小写不敏感，认 Kubernetes 全名与 k8s 缩写（含后缀名）；
// 空名（平台没解析出来）不匹配——门在证据上拒绝，不在证据缺席上。
func TestIsK8sPlatform(t *testing.T) {
	for _, name := range []string{"Kubernetes", "kubernetes", "K8S", "k8s-prod", " Kubernetes-Cluster "} {
		if !isK8sPlatform(name) {
			t.Errorf("isK8sPlatform(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "Linux", "Gateway", "Windows", "k3s-not-a-platform-name"} {
		if isK8sPlatform(name) {
			t.Errorf("isK8sPlatform(%q) = true, want false", name)
		}
	}
}
