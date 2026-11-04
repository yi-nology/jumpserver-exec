package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yi-nology/jumpserver-mcp-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-mcp-exec/internal/service/policy"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/testsupport"
)

// The collect tools carry the CALLER's commands, so they are command runners
// and have to be judged like one.
//
// This is the kind of classification that rots quietly: the tool is named
// "collect", it is used for read-only sweeps, and it looks harmless in a
// catalogue — until somebody notices that its checks are the caller's own
// commands.
func TestCollectWithADangerousCommandIsBlocked(t *testing.T) {
	// No bastion is needed: the refusal happens before a session is opened.
	cs := testsupport.NewSession(t, "collect-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterTransferTools(r, mustLoad(t)) })

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "jmsx_collect",
		Arguments: map[string]any{
			"asset":  "10.0.0.1",
			"checks": []map[string]any{{"name": "disk", "command": "rm -rf /var/lib/x"}},
		},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatal("the collect was refused as an error; it should answer with the plan it did not carry out")
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("unparsable answer: %v\n%s", err, text)
	}
	if out["blocked"] == nil {
		t.Errorf("the dangerous check was not blocked: %s", text)
	}
	if out["risk"] != "DANGEROUS" {
		t.Errorf("risk = %v, want DANGEROUS", out["risk"])
	}
}

// The contract: the collect result carries the risk class and the reason it
// was not carried out.
func TestCommandResultCarriesTheBlockedReason(t *testing.T) {
	res := &jmsexec.CommandResult{
		Asset:    "10.0.0.1",
		Command:  "rm -rf /",
		ExitCode: -1,
		Via:      "jumpserver koko (bastion), recorded",
	}
	if res.GetExitCode() != -1 {
		t.Errorf("exit code = %d, want -1 (no command ran)", res.GetExitCode())
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["blocked"]; ok {
		t.Error("an executed result must not carry a blocked reason")
	}
}
