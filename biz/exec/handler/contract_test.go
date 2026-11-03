package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yi-nology/jumpserver-mcp-exec/biz/exec/model/jmsexec"
	"github.com/yi-nology/jumpserver-mcp-exec/biz/exec/policy"
	"github.com/yi-nology/jumpserver-mcp-exec/conf/exec"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/testsupport"
)

// Every tool must name the fields it answers with.
//
// render.Object is the escape hatch: it says "an object" and nothing else,
// so the answer is not checked at all — a tool can quietly stop returning a
// field and nobody finds out. The execution plane has no tool left that
// needs it, so this guard keeps it that way: a new tool gets a contract
// type in the IDL, not the vague shape.
func TestNoToolFallsBackToTheVagueShape(t *testing.T) {
	cs := testsupport.NewSession(t, "shape-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) {
			RegisterExecTools(r, mustLoad(t))
			RegisterTransferTools(r, mustLoad(t))
			if c := mustLoad(t).APIClient(); c != nil {
				RegisterInventoryTools(r, c)
			}
		})

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools listed")
	}
	for _, tool := range res.Tools {
		schema, ok := tool.OutputSchema.(map[string]any)
		if !ok {
			t.Errorf("tool %s declares no output schema", tool.Name)
			continue
		}
		props, _ := schema["properties"].(map[string]any)
		if len(props) == 0 {
			t.Errorf("tool %s declares a shape with no fields of its own: "+
				"its answer is not checked", tool.Name)
		}
	}
}

// The refusal and the rehearsal are answers too, and they have to be
// answers of the same contract as the run they stand in for.
//
// This is where a contract earns its keep: the server checks the answer
// against the schema before the model sees it, so an answer that does not
// match fails the call right here rather than reaching the agent in a shape
// nobody promised.
func TestRefusalsAndRehearsalsAnswerTheirOwnContract(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{
			name: "a batch run of a destructive command",
			tool: "jmsx_run",
			args: map[string]any{
				"asset":    "10.0.0.1",
				"commands": []string{"rm -rf /var/lib/x"},
			},
		},
		{
			name: "a rehearsal of a batch run",
			tool: "jmsx_run",
			args: map[string]any{
				"asset":    "10.0.0.1",
				"commands": []string{"uptime"},
				"dry_run":  true,
			},
		},
		{
			name: "a rehearsal of a collect",
			tool: "jmsx_collect",
			args: map[string]any{
				"asset":   "10.0.0.1",
				"checks":  []map[string]any{{"name": "disk", "command": "df -h /"}},
				"dry_run": true,
			},
		},
		{
			name: "a rehearsal of a sweep",
			tool: "jmsx_exec_multi",
			args: map[string]any{
				"assets":  []string{"host-a", "host-b"},
				"command": "uptime",
				"dry_run": true,
			},
		},
		{
			name: "a background job that was refused",
			tool: "jmsx_bg_start",
			args: map[string]any{
				"asset":   "10.0.0.1",
				"command": "rm -rf /var/lib/x",
			},
		},
	}
	for _, tc := range cases {
		out := callTool(t, tc.tool, tc.args)
		if out["blocked"] == nil && out["dry_run"] == nil {
			t.Errorf("%s: the answer says neither that it was refused nor that it was a rehearsal: %v",
				tc.name, out)
		}
	}
}

// The rehearsal of a batch must not carry results: nothing ran, so there is
// nothing to report. A "results" field that appears in every answer but
// means something different in each one is worse than no field at all.
func TestARehearsalCarriesNoResults(t *testing.T) {
	out := callTool(t, "jmsx_run", map[string]any{
		"asset":    "10.0.0.1",
		"commands": []string{"uptime"},
		"dry_run":  true,
	})
	if _, ok := out["results"]; ok {
		t.Errorf("the rehearsal carries results: %v", out)
	}
	// The commands it would run are still named — that is the point of a
	// rehearsal.
	cmds, _ := out["commands"].([]any)
	if len(cmds) != 1 {
		t.Errorf("the rehearsal does not name the commands: %v", out)
	}
}

// The contract is read from the wire, not from the Go value: what the
// caller receives is JSON, and the two spellings have to agree.
func TestTheRehearsalMatchesTheContractType(t *testing.T) {
	out := callTool(t, "jmsx_collect", map[string]any{
		"asset":   "10.0.0.1",
		"checks":  []map[string]any{{"name": "disk", "command": "df -h /"}},
		"dry_run": true,
	})
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var res jmsexec.CollectResult
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, b)
	}
	if res.GetAsset() != "10.0.0.1" {
		t.Errorf("asset = %q, want 10.0.0.1", res.GetAsset())
	}
	if !res.GetDryRun() {
		t.Error("the answer does not say it was a rehearsal")
	}
	if res.GetWouldExecute() != true {
		t.Errorf("would_execute = %v, want true (the check is harmless)", res.GetWouldExecute())
	}
	if len(res.GetChecks()) != 1 {
		t.Fatalf("the rehearsal does not carry the checks: %s", b)
	}
	if res.GetChecks()[0].GetName() != "disk" {
		t.Errorf("the check lost its name: %s", b)
	}
}

// mustLoad reads the execution plane's configuration, failing the test if it
// cannot be read.
func mustLoad(t *testing.T) conf.Config {
	t.Helper()
	return testsupport.MustLoad(t, conf.Load)
}
