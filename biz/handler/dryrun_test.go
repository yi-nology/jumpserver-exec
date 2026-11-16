package handler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/service/policy"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/risk"
	"github.com/yi-nology/jumpserver-kit/pkg/testsupport"
)

// A dry run must have NO side effect: jmsx_exec with dry_run=true and
// confirm=true rehearses "it would run" but grants NOTHING. The follow-up
// REAL call on the identical (asset, command) without confirm is refused
// exactly as if the rehearsal never happened. The control shows the real
// confirm=true DOES grant: the next real call without confirm passes the
// gate (it fails later only because no bastion is configured — which is
// how the test tells "gate satisfied" from "gate refused").
func TestExecDryRunDoesNotGrantTheApprovalMemory(t *testing.T) {
	cs := testsupport.NewSession(t, "exec-dryrun-memory", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterExecTools(r, mustLoad(t), policy.NewMemory(10*time.Minute)) })

	rehearsed := testsupport.CallToolJSON(t, cs, "jmsx_exec", map[string]any{
		"asset": "10.0.0.1", "command": "rm -rf /var/lib/x", "dry_run": true, "confirm": true,
	})
	if rehearsed["would_execute"] != true {
		t.Fatalf("the rehearsal did not allow the confirmed command: %v", rehearsed)
	}

	// The real call without confirm: refused — the dry run granted nothing.
	refused := testsupport.CallToolJSON(t, cs, "jmsx_exec", map[string]any{
		"asset": "10.0.0.1", "command": "rm -rf /var/lib/x",
	})
	if blocked, _ := refused["blocked"].(string); !strings.Contains(blocked, "confirm=true") {
		t.Fatalf("the dry run granted the approval — the real call was not refused: %v", refused)
	}

	// Control: the real confirm=true grants. The gate is satisfied (the
	// call dies one step later, on the missing bastion — NOT a refusal).
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "jmsx_exec",
		Arguments: map[string]any{"asset": "10.0.0.1", "command": "rm -rf /var/lib/x", "confirm": true},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError || !strings.Contains(testsupport.ToolText(t, res), "every session backend failed") {
		t.Fatalf("the real confirmed call did not reach the session layer: %s", testsupport.ToolText(t, res))
	}
	// And the memory now satisfies the gate for the identical call.
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "jmsx_exec",
		Arguments: map[string]any{"asset": "10.0.0.1", "command": "rm -rf /var/lib/x"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError || !strings.Contains(testsupport.ToolText(t, res), "every session backend failed") {
		t.Fatalf("the remembered approval did not satisfy the gate: %s", testsupport.ToolText(t, res))
	}
}

// The same no-grant rule on a multi-host tool: jmsx_exec_multi with
// dry_run=true and confirm=true rehearses the sweep but writes no
// approval; the real sweep without confirm is refused at the gate.
func TestExecMultiDryRunDoesNotGrantTheApprovalMemory(t *testing.T) {
	cs := testsupport.NewSession(t, "execmulti-dryrun-memory", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterTransferTools(r, mustLoad(t), policy.NewMemory(10*time.Minute)) })

	rehearsed := testsupport.CallToolJSON(t, cs, "jmsx_exec_multi", map[string]any{
		"assets": []string{"10.0.0.1"}, "command": "rm -rf /var/lib/x", "dry_run": true, "confirm": true,
	})
	if rehearsed["would_execute"] != true {
		t.Fatalf("the rehearsal did not allow the confirmed sweep: %v", rehearsed)
	}

	refused := testsupport.CallToolJSON(t, cs, "jmsx_exec_multi", map[string]any{
		"assets": []string{"10.0.0.1"}, "command": "rm -rf /var/lib/x",
	})
	if blocked, _ := refused["blocked"].(string); !strings.Contains(blocked, "confirm=true") {
		t.Fatalf("the dry run granted the approval — the real sweep was not refused: %v", refused)
	}

	// Control: the real confirm=true grants; the identical sweep without
	// confirm now passes the gate and reaches the per-host session layer
	// (the host row fails on the missing bastion — NOT a gate refusal).
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "jmsx_exec_multi",
		Arguments: map[string]any{
			"assets": []string{"10.0.0.1"}, "command": "rm -rf /var/lib/x", "confirm": true,
		},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("the real confirmed sweep did not reach the sweep layer: %s", testsupport.ToolText(t, res))
	}
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "jmsx_exec_multi",
		Arguments: map[string]any{
			"assets": []string{"10.0.0.1"}, "command": "rm -rf /var/lib/x",
		},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("the remembered approval did not satisfy the gate: %s", testsupport.ToolText(t, res))
	}
}

// A dry run is the cheap question: what is this command classified as, and
// would it run? It must cost no bastion session and have no side effect —
// which is why the test needs no bastion at all: a dry run returns before
// the session is opened.
func TestDryRunAnswersWithoutRunning(t *testing.T) {
	cs := testsupport.NewSession(t, "dryrun-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterExecTools(r, mustLoad(t), nil) })

	cases := []struct {
		name         string
		command      string
		confirm      bool
		wantRisk     string
		wantWouldRun bool
		wantBlocked  bool
	}{
		{
			name:         "a harmless command would run",
			command:      "uptime",
			wantRisk:     "READ",
			wantWouldRun: true,
		},
		{
			name:         "a destructive command without confirmation would not run",
			command:      "rm -rf /var/lib/x",
			wantRisk:     "DANGEROUS",
			wantWouldRun: false,
			wantBlocked:  true,
		},
		{
			name:         "a destructive command with confirmation would run",
			command:      "rm -rf /var/lib/x",
			confirm:      true,
			wantRisk:     "DANGEROUS",
			wantWouldRun: true,
		},
	}
	for _, tc := range cases {
		out := testsupport.CallToolJSON(t, cs, "jmsx_exec", map[string]any{
			"asset":   "10.0.0.1",
			"command": tc.command,
			"dry_run": true,
			"confirm": tc.confirm,
		})
		if out["dry_run"] != true {
			t.Errorf("%s: dry_run = %v, want true", tc.name, out["dry_run"])
		}
		if out["risk"] != tc.wantRisk {
			t.Errorf("%s: risk = %v, want %s", tc.name, out["risk"], tc.wantRisk)
		}
		if out["would_execute"] != tc.wantWouldRun {
			t.Errorf("%s: would_execute = %v, want %v", tc.name, out["would_execute"], tc.wantWouldRun)
		}
		if blocked, _ := out["blocked"].(string); (blocked != "") != tc.wantBlocked {
			t.Errorf("%s: blocked = %q, want blocked=%v", tc.name, blocked, tc.wantBlocked)
		}
	}
}

// The confirmation gate with the approval memory: a readonly command never
// asks and never notes; a dangerous one is blocked without confirm unless a
// live remembered approval covers this EXACT command on this EXACT asset;
// confirm=true grants such an approval and says so in the note.
func TestRehearsalM(t *testing.T) {
	mem := policy.NewMemory(10 * time.Minute)
	dangerous := risk.Classify("rm -rf /tmp/x")
	ro := risk.Classify("ls /tmp")
	// 只读命令从不确认、不记笔记
	if rc, would, blocked, note := rehearsalM(mem, "a", "ls /tmp", ro, false, false); !would || blocked != "" || note != "" {
		t.Fatalf("readonly: would=%v blocked=%q note=%q", would, blocked, note)
	} else if rc != ro.String() {
		t.Fatalf("readonly: riskClass = %q, want %q", rc, ro.String())
	}
	// 危险无 confirm 无记忆 ⇒ 拦
	if _, would, blocked, _ := rehearsalM(nil, "a", "rm -rf /tmp/x", dangerous, false, false); would || blocked == "" {
		t.Fatal("dangerous without confirm must be blocked when no memory")
	}
	// confirm=true ⇒ 放行 + 记笔记 + 授予
	_, would, _, note := rehearsalM(mem, "a", "rm -rf /tmp/x", dangerous, true, false)
	if !would || !strings.Contains(note, "remembered") {
		t.Fatalf("grant note missing: would=%v note=%q", would, note)
	}
	// 同命令同资产无 confirm ⇒ 记忆放行 + 笔记写明来源
	_, would, blocked, note := rehearsalM(mem, "a", "rm -rf /tmp/x", dangerous, false, false)
	if !would || blocked != "" || !strings.Contains(note, "remembered approval") {
		t.Fatalf("memory hit failed: would=%v blocked=%q note=%q", would, blocked, note)
	}
	// 别的资产不命中
	if _, would, _, _ := rehearsalM(mem, "b", "rm -rf /tmp/x", dangerous, false, false); would {
		t.Fatal("memory leaked across assets")
	}
	// 记忆关闭（TTL=0）⇒ 就算先前授予过也不放行
	off := policy.NewMemory(0)
	off.Grant("a", "rm -rf /tmp/x")
	if _, would, blocked, _ := rehearsalM(off, "a", "rm -rf /tmp/x", dangerous, false, false); would || blocked == "" {
		t.Fatal("a disabled memory must not satisfy the gate")
	}
}

// A rehearsal must have NO side effect: confirm=true on the dry-run path
// answers "it would run" but grants nothing — the approval memory is
// written only by the real call. The verdict note says so explicitly.
func TestRehearsalMDryRunGrantsNothing(t *testing.T) {
	mem := policy.NewMemory(10 * time.Minute)
	dangerous := risk.Classify("rm -rf /tmp/x")
	// dry-run + confirm: would run, and the note says nothing was granted
	_, would, blocked, note := rehearsalM(mem, "a", "rm -rf /tmp/x", dangerous, true, true)
	if !would || blocked != "" {
		t.Fatalf("dry-run confirm: would=%v blocked=%q", would, blocked)
	}
	if !strings.Contains(note, "nothing granted") {
		t.Fatalf("the dry-run note does not say nothing was granted: %q", note)
	}
	// the follow-up real call WITHOUT confirm must be refused: the dry run
	// granted nothing
	_, would, blocked, _ = rehearsalM(mem, "a", "rm -rf /tmp/x", dangerous, false, false)
	if would || blocked == "" {
		t.Fatal("the dry run granted the approval: the real call without confirm was let through")
	}
	// control: the real call WITH confirm grants, and the next real call
	// without confirm is satisfied by the memory
	_, would, _, _ = rehearsalM(mem, "a", "rm -rf /tmp/x", dangerous, true, false)
	if !would {
		t.Fatal("real confirm was not let through")
	}
	_, would, blocked, note = rehearsalM(mem, "a", "rm -rf /tmp/x", dangerous, false, false)
	if !would || blocked != "" || !strings.Contains(note, "remembered approval") {
		t.Fatalf("real grant not remembered: would=%v blocked=%q note=%q", would, blocked, note)
	}
}

// The contract carries the dry-run fields, so the shape of the answer is
// owned by the IDL rather than assembled per call.
func TestCommandResultCarriesTheDryRunFields(t *testing.T) {
	res := dryRunCommand(nil, "10.0.0.1", "uptime", jms.CommandClass_READ, false)
	if res.GetDryRun() != true {
		t.Error("dry_run is not set")
	}
	if res.GetWouldExecute() != true {
		t.Error("would_execute is not set")
	}
	if res.GetNote() == "" {
		t.Error("the dry run says nothing about itself")
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["dry_run"] != true || out["would_execute"] != true {
		t.Errorf("the wire form lost the dry-run fields: %s", b)
	}
	_ = jmsexec.CommandResult{}
}

// A put_dir rehearsal answers from the LOCAL prescan alone: the call must
// not reach for a session (there is no bastion in this test, so a session
// attempt would fail the call outright), and the planning numbers are the
// tree's own (issue #20).
//
// The confirm flag is still required by the tool's registry gate — the flag
// admits that the tool may write; the rehearsal itself writes nothing.
func TestPutDirDryRunAnswersFromTheLocalPrescan(t *testing.T) {
	cs := testsupport.NewSession(t, "dryrun-putdir", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterTransferTools(r, mustLoad(t), nil) })

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.conf"), []byte("listen :8080"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	out := testsupport.CallToolJSON(t, cs, "jmsx_put_dir", map[string]any{
		"asset":       "10.0.0.1",
		"local_path":  dir,
		"remote_path": "/opt/app",
		"dry_run":     true,
		"confirm":     true,
	})
	if out["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", out["dry_run"])
	}
	if out["would_execute"] != true {
		t.Errorf("would_execute = %v, want true (a finished prescan is the green light)", out["would_execute"])
	}
	if out["risk"] != "destructive" {
		t.Errorf("risk = %v, want the catalogue class", out["risk"])
	}
	if out["files"] != float64(2) {
		t.Errorf("files = %v, want 2", out["files"])
	}
	if out["bytes"] != float64(len("listen :8080")+len("#!/bin/sh\n")) {
		t.Errorf("bytes = %v, want the content total", out["bytes"])
	}
	if _, ok := out["stats"]; ok {
		t.Errorf("the rehearsal carries session stats: %v", out)
	}
	note, _ := out["note"].(string)
	if !strings.Contains(note, "local prescan") {
		t.Errorf("the note does not say the numbers came from a prescan: %s", note)
	}
}

// A prescan of a path that is not a directory is refused with the error's
// own text — the rehearsal must not answer about a tree the upload could
// not walk either.
func TestPutDirDryRunRefusesANonDirectory(t *testing.T) {
	cs := testsupport.NewSession(t, "dryrun-putdir-bad", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterTransferTools(r, mustLoad(t), nil) })
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "jmsx_put_dir",
		Arguments: map[string]any{
			"asset":       "10.0.0.1",
			"local_path":  file,
			"remote_path": "/opt/app",
			"dry_run":     true,
			"confirm":     true,
		},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatalf("a file was rehearsed as a directory: %s", testsupport.ToolText(t, res))
	}
	if txt := testsupport.ToolText(t, res); !strings.Contains(txt, "is not a directory") {
		t.Errorf("the refusal does not say what was wrong: %s", txt)
	}
}
