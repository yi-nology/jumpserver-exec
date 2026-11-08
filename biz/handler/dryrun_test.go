package handler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/service/policy"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/testsupport"
)

// A dry run is the cheap question: what is this command classified as, and
// would it run? It must cost no bastion session and have no side effect —
// which is why the test needs no bastion at all: a dry run returns before
// the session is opened.
func TestDryRunAnswersWithoutRunning(t *testing.T) {
	cs := testsupport.NewSession(t, "dryrun-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterExecTools(r, mustLoad(t)) })

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

// The contract carries the dry-run fields, so the shape of the answer is
// owned by the IDL rather than assembled per call.
func TestCommandResultCarriesTheDryRunFields(t *testing.T) {
	res := dryRunCommand("10.0.0.1", "uptime", jms.CommandClass_READ, false)
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
		func(r *reg.Registry) { RegisterTransferTools(r, mustLoad(t)) })

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
		func(r *reg.Registry) { RegisterTransferTools(r, mustLoad(t)) })
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
