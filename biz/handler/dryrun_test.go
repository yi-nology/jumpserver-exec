package handler

import (
	"encoding/json"
	"testing"

	"github.com/yi-nology/jumpserver-mcp-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-mcp-exec/internal/service/policy"
	"github.com/yi-nology/jumpserver-mcp-kit/model/jms"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/testsupport"
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
