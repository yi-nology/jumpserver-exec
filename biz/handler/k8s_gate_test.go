package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-exec/internal/service/policy"
	"github.com/yi-nology/jumpserver-kit/pkg/jmsapi"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/testsupport"
)

// k8sBastionConfig serves a bastion listing with one Kubernetes asset and
// one Linux asset. The platform gate refuses before anything is dialed, so
// the fixture needs no koko and no SSH — the listing is the whole bastion.
func k8sBastionConfig(t *testing.T) config.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"count":2,"results":[`+
			`{"id":"id-studio-prod","name":"studio-prod","address":"10.0.0.9","platform":{"name":"Kubernetes"}},`+
			`{"id":"id-node-031","name":"node-031","address":"10.0.0.31","platform":{"name":"Linux"}}]}`)
	}))
	t.Cleanup(srv.Close)
	return config.Config{Koko: koko.Config{
		API: jmsapi.New(jmsapi.Options{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()}),
	}}
}

// jmsx_reachable 对 K8s 资产的假绿灯（exec#31，2026-11-11 v5 实测：
// reachable=true、whoami 是 koko 的拒绝文案）。平台的门开起来之后，前置
// 检查的答复必须是**诚实的不可达**：reachable=false + error 指引跳板机
// kubectl + delivery=not_sent。答复仍是工具的一次正常返回（不是调用级
// 错误）——前置检查的职责是报告，不是拒绝调用。
func TestReachableReportsK8sAssetsAsUnshellable(t *testing.T) {
	cfg := k8sBastionConfig(t)
	cs := testsupport.NewSession(t, "k8s-reachable", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterExecTools(r, cfg); RegisterTransferTools(r, cfg) })

	out := testsupport.CallToolJSON(t, cs, "jmsx_reachable", map[string]any{"asset": "studio-prod"})
	if reachable, _ := out["reachable"].(bool); reachable {
		t.Fatalf("jmsx_reachable answered %v — the fake green light that sends a whole batch into fake successes", out["reachable"])
	}
	errMsg, _ := out["error"].(string)
	for _, want := range []string{"studio-prod", "Kubernetes", "kubectl", "jmsx_exec"} {
		if !strings.Contains(errMsg, want) {
			t.Errorf("the error must name %q, got: %q", want, errMsg)
		}
	}
	if d, _ := out["delivery"].(string); d != "not_sent" {
		t.Errorf("delivery = %q, want not_sent (nothing left this process)", d)
	}
}

// jmsx_exec 对 K8s 资产是调用级失败（exec#29）：明确 fail 并指引跳板机
// kubectl，而不是「exit 0 + 空输出」的假成功。
func TestExecRefusesK8sAssetsWithTheJumpHostGuidance(t *testing.T) {
	cfg := k8sBastionConfig(t)
	cs := testsupport.NewSession(t, "k8s-exec", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterExecTools(r, cfg); RegisterTransferTools(r, cfg) })

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "jmsx_exec",
		Arguments: map[string]any{"asset": "studio-prod", "command": "kubectl get nodes"},
	})
	if err != nil {
		t.Fatalf("call jmsx_exec: %v", err)
	}
	if !res.IsError {
		t.Fatal("jmsx_exec on a kubernetes asset must be a call-level failure, not a fake success")
	}
	txt := testsupport.ToolText(t, res)
	for _, want := range []string{"studio-prod", "Kubernetes", "kubectl", "jmsx_exec"} {
		if !strings.Contains(txt, want) {
			t.Errorf("the refusal must name %q, got: %q", want, txt)
		}
	}
}

// 多机采集的逐台答复（exec#29 的清单里有 jmsx_collect_multi）：门是逐台的
// ——K8s 资产那一行的 error 带指引、delivery=not_sent；Linux 那一行不被
// 平台门拦（它的失败是夹具里没有的下一站，与平台无关）。
func TestCollectMultiRowsCarryTheRefusalPerHost(t *testing.T) {
	cfg := k8sBastionConfig(t)
	cs := testsupport.NewSession(t, "k8s-collect", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterExecTools(r, cfg); RegisterTransferTools(r, cfg) })

	out := testsupport.CallToolJSON(t, cs, "jmsx_collect_multi", map[string]any{
		"assets": []string{"studio-prod", "node-031"},
		"checks": []map[string]string{{"name": "host", "command": "hostname"}},
	})
	matrix, _ := out["matrix"].(map[string]any)
	if matrix == nil {
		t.Fatalf("the answer carries no matrix: %v", out)
	}
	k8sRow, _ := matrix["studio-prod"].(map[string]any)
	if k8sRow == nil {
		t.Fatalf("the matrix has no studio-prod row: %v", matrix)
	}
	if ok, _ := k8sRow["ok"].(bool); ok {
		t.Errorf("the k8s row must not be ok: %v", k8sRow)
	}
	rowErr, _ := k8sRow["error"].(string)
	for _, want := range []string{"studio-prod", "kubectl"} {
		if !strings.Contains(rowErr, want) {
			t.Errorf("the k8s row's error must name %q, got: %q", want, rowErr)
		}
	}
	if d, _ := k8sRow["delivery"].(string); d != "not_sent" {
		t.Errorf("the k8s row's delivery = %q, want not_sent", d)
	}
	// 对照组：Linux 行不因平台被拒。
	linuxRow, _ := matrix["node-031"].(map[string]any)
	if linuxRow == nil {
		t.Fatalf("the matrix has no node-031 row: %v", matrix)
	}
	linuxErr, _ := linuxRow["error"].(string)
	if strings.Contains(linuxErr, "kubernetes asset has no shell") {
		t.Errorf("the Linux row must not carry the platform refusal: %q", linuxErr)
	}
}
