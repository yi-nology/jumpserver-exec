package handler

import (
	"context"
	"fmt"
	"github.com/yi-nology/jumpserver-mcp-kit/model/jms"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yi-nology/jumpserver-mcp-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-mcp-exec/internal/service/policy"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/audit"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/render"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/testsupport"
)

// The tool has to answer with the contract it declares, and the contract has
// to carry the REFUSAL — because that line is the one nothing else has.
//
// A command the gate refused never reached the bastion, so the bastion has no
// session and no log line for it. This is the only place that fact exists.
func TestAuditQueryAnswersTheLinesItFound(t *testing.T) {
	dir := t.TempDir()
	settings := audit.Settings{Mode: "file", Dir: dir}
	writeLine(t, settings, `{"ts":"2026-10-28T09:00:00+08:00","service":"jumpserver-mcp-exec",`+
		`"tool":"jmsx_exec","args":"{\"command\":\"rm -rf /var/lib/x\"}",`+
		`"allowed":false,"refused":"classified DANGEROUS",`+
		`"stages":[{"stage":"capability","allow":true,"reason":"白名单放行: jmsx_exec"}],`+
		`"duration_ms":3,"out_bytes":0}`)

	cs := testsupport.NewSession(t, "audit-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterAuditTools(r, settings) })

	out := testsupport.CallToolJSON(t, cs, "jmsx_audit_query", map[string]any{})
	if out["trail_dir"] != dir {
		t.Errorf("the answer says the trail is in %v, want %s", out["trail_dir"], dir)
	}
	entries, ok := out["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("the answer carries no entry: %+v", out)
	}
	e, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("the entry is not an object: %+v", entries[0])
	}
	if e["tool"] != "jmsx_exec" {
		t.Errorf("the entry says the tool is %v", e["tool"])
	}
	if e["service"] != "jumpserver-mcp-exec" {
		t.Errorf("the entry says the service is %v", e["service"])
	}
	if e["allowed"] != false {
		t.Errorf("the entry says the call was allowed: %v", e["allowed"])
	}
	if e["refused"] != "classified DANGEROUS" {
		t.Errorf("the entry does not carry the refusal: %v", e["refused"])
	}
	stages, ok := e["stages"].([]any)
	if !ok || len(stages) != 1 {
		t.Fatalf("the entry carries no stage: %+v", e["stages"])
	}
	st, ok := stages[0].(map[string]any)
	if !ok {
		t.Fatalf("the stage is not an object: %+v", stages[0])
	}
	if st["stage"] != "risk" && st["stage"] != "capability" {
		t.Errorf("the stage says %+v", st)
	}
}

// The count of what matched is not the count of what was returned: a caller
// that reports "50 of 300" has to be able to tell.
func TestAuditQueryReportsTheCountAndTheTotal(t *testing.T) {
	dir := t.TempDir()
	settings := audit.Settings{Mode: "file", Dir: dir}
	for i := 0; i < 5; i++ {
		writeLine(t, settings, `{"ts":"2026-10-28T09:00:0`+string(rune('0'+i))+`+08:00",`+
			`"service":"jumpserver-mcp-exec","tool":"jmsx_exec","allowed":true}`)
	}

	cs := testsupport.NewSession(t, "audit-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterAuditTools(r, settings) })

	out := testsupport.CallToolJSON(t, cs, "jmsx_audit_query", map[string]any{"limit": 2})
	if out["count"] != float64(2) {
		t.Errorf("the answer says it returned %v lines, want 2", out["count"])
	}
	if out["total"] != float64(5) {
		t.Errorf("the answer says %v lines matched, want 5", out["total"])
	}
	if out["truncated"] != true {
		t.Errorf("the answer does not say it was truncated: %v", out["truncated"])
	}
}

// An empty trail is an answer, not an error: there is a difference between
// "nothing happened" and "the question could not be answered".
func TestAuditQueryAnswersAnEmptyTrail(t *testing.T) {
	settings := audit.Settings{Mode: "file", Dir: t.TempDir()}
	cs := testsupport.NewSession(t, "audit-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterAuditTools(r, settings) })

	out := testsupport.CallToolJSON(t, cs, "jmsx_audit_query", map[string]any{})
	if out["total"] != float64(0) {
		t.Errorf("the answer says the trail holds %v lines, want 0", out["total"])
	}
	if out["trail_dir"] == nil || out["trail_dir"] == "" {
		t.Error("the answer does not say where the trail is")
	}
}

// The delivery filter answers the most dangerous question in the trail:
// "what may still be running on that machine?".
//
// And a value that is not one of the two states is a MISTAKE — it must not
// quietly match nothing, because "nothing may still be running out there"
// is the most dangerous silence this tool could produce.
func TestTheDeliveryFilterAnswersAndRefusesNonsense(t *testing.T) {
	dir := t.TempDir()
	settings := audit.Settings{Mode: "file", Dir: dir}
	writeLine(t, settings, `{"ts":"2026-10-28T09:00:00+08:00","service":"jumpserver-mcp-exec",`+
		`"tool":"jmsx_exec","args":"{\"command\":\"rm -rf /var/lib/x\"}",`+
		`"allowed":true,"failed":true,"delivery":"maybe_sent",`+
		`"error":"the command may already be running on the machine"}`)
	writeLine(t, settings, `{"ts":"2026-10-28T09:00:01+08:00","service":"jumpserver-mcp-exec",`+
		`"tool":"jmsx_exec","args":"{\"command\":\"uptime\"}",`+
		`"allowed":true}`)

	cs := testsupport.NewSession(t, "audit-delivery-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterAuditTools(r, settings) })

	out := testsupport.CallToolJSON(t, cs, "jmsx_audit_query", map[string]any{
		"delivery": "maybe_sent",
	})
	if out["total"] != float64(1) {
		t.Errorf("the answer says %v lines may still be running, want 1", out["total"])
	}
	entries, _ := out["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("the answer carries %d lines, want 1", len(entries))
	}
	row, _ := entries[0].(map[string]any)
	if row["delivery"] != "maybe_sent" {
		t.Errorf("the line says the delivery state is %v, want maybe_sent", row["delivery"])
	}

	// A value that is not one of the two states is refused, loudly.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "jmsx_audit_query",
		Arguments: map[string]any{"delivery": "maybe sent"},
	})
	if err != nil {
		t.Fatalf("call jmsx_audit_query: %v", err)
	}
	if !res.IsError {
		t.Fatalf("a nonsense delivery state was accepted: %s", testsupport.ToolText(t, res))
	}
	if txt := testsupport.ToolText(t, res); !strings.Contains(txt, "not_sent") {
		t.Errorf("the refusal does not say what the states are: %s", txt)
	}
}

// A dangerous command without a confirmation is refused by the TOOL, not by
// the gate — and the bastion knows nothing about it, because the command
// never got there.
//
// This is the case the trail exists for, so it gets a test of its own: the
// record has to carry the refusal, or the trail would say the command ran.
func TestARefusedCommandIsRecordedAsARefusal(t *testing.T) {
	var got []reg.Record
	cs := testsupport.NewSession(t, "audit-refusal-test",
		reg.Options{
			Policy: policy.Table,
			OnRecord: func(r reg.Record) {
				got = append(got, r)
			},
		},
		func(r *reg.Registry) { RegisterExecTools(r, mustLoad(t)) })

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "jmsx_exec",
		Arguments: map[string]any{
			"asset":   "10.0.0.1",
			"command": "rm -rf /var/lib/x",
		},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("the call failed: %s", testsupport.ToolText(t, res))
	}
	if len(got) != 1 {
		t.Fatalf("the gate recorded %d calls, want 1", len(got))
	}
	rec := got[0]
	if !rec.Gate.Refused() {
		t.Error("the refusal is not in the record: the trail would say the command ran")
	}
	if reason := rec.Gate.Outcome.RefusalReason(); !strings.Contains(reason, "DANGEROUS") {
		t.Errorf("the refusal does not say why: %q", reason)
	}
	// A refusal is an attempt that was stopped — and the trail reads it
	// that way only when it is not a rehearsal (the trail's own reading
	// of the two is pinned in pkg/audit's test).
	if !(rec.Gate.Refused() && !rec.Gate.DryRun()) {
		t.Error("the record does not say the call was stopped")
	}
	if rec.Gate.Acted() {
		t.Error("a refusal counts as something that acted")
	}
	if !rec.Gate.Allowed {
		t.Error("the gate refused it; the refusal is the tool's, and the two are told apart")
	}
}

// A failure reported INSIDE the answer must reach the trail.
//
// Some tools answer with the results of the commands that worked and say
// what broke the rest. That answer is a SUCCESSFUL call — the chain sees no
// error — so without the trail reading the answer's own account of the
// failure, a broken command reads exactly like a clean success, and the
// "what may still be running on that machine?" question comes back empty
// for the very calls it matters most for.
func TestAFailureReportedInTheAnswerReachesTheTrail(t *testing.T) {
	var got []reg.Record
	cs := testsupport.NewSession(t, "audit-answer-failure-test",
		reg.Options{
			Policy: policy.Table,
			OnRecord: func(r reg.Record) {
				got = append(got, r)
			},
		},
		func(r *reg.Registry) {
			reg.Add(r, &mcp.Tool{
				Name:         "jmsx_run",
				Description:  "a tool that reports the failure in its answer",
				OutputSchema: render.Schema[jmsexec.BatchResult](),
			}, func(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
				return render.Text(&jmsexec.BatchResult{
					Asset:    "10.0.0.1",
					Results:  []string{"uptime output"},
					Error:    jms.Opt("the command may already be running on the machine"),
					Partial:  jms.OptBool(true),
					Delivery: jms.Opt("maybe_sent"),
				})
			})
		})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "jmsx_run",
		Arguments: map[string]any{"asset": "10.0.0.1", "commands": []string{"uptime"}},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("the call failed: %s", testsupport.ToolText(t, res))
	}
	if len(got) != 1 {
		t.Fatalf("the gate recorded %d calls, want 1", len(got))
	}
	rec := got[0]
	if rec.Failure == "" {
		t.Error("the record does not carry the failure the answer reported")
	}
	if rec.Delivery != "maybe_sent" {
		t.Errorf("the record says the delivery state is %q, want maybe_sent", rec.Delivery)
	}
}

// A rehearsal is not a refusal.
//
// The rehearsal's answer carries a blocked field too — the hint of what
// would happen with a confirmation — and the trail must not read that as a
// refusal: recording a rehearsal as an attempt that was stopped is a false
// alarm on the very question the trail exists to answer.
func TestARehearsalIsNotRecordedAsARefusal(t *testing.T) {
	var got []reg.Record
	cs := testsupport.NewSession(t, "audit-rehearsal-test",
		reg.Options{
			Policy: policy.Table,
			OnRecord: func(r reg.Record) {
				got = append(got, r)
			},
		},
		func(r *reg.Registry) { RegisterExecTools(r, mustLoad(t)) })

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "jmsx_exec",
		Arguments: map[string]any{
			"asset":   "10.0.0.1",
			"command": "rm -rf /var/lib/x",
			"dry_run": true,
		},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("the call failed: %s", testsupport.ToolText(t, res))
	}
	if len(got) != 1 {
		t.Fatalf("the gate recorded %d calls, want 1", len(got))
	}
	rec := got[0]
	if !rec.Gate.DryRun() {
		t.Error("the record does not say the call was a rehearsal")
	}
	// The record carries the refusal hint, but a rehearsal is not a
	// stopped attempt: the two are read separately, and the trail's
	// reading of them is pinned in pkg/audit's test.
	if rec.Gate.Refused() && !rec.Gate.DryRun() {
		t.Errorf("the record calls a rehearsal a refusal: %q", rec.Gate.Outcome.RefusalReason())
	}
	if rec.Gate.Acted() {
		t.Error("a rehearsal counts as something that acted")
	}
}

// The refusal of an answer has to be FINDABLE.
//
// The trail reads it by name — the contract's `blocked` field — and a name
// that stops matching does not fail loudly: it just makes the trail say
// "the command ran" for every dangerous command that was stopped. So the
// contract types that can refuse are pinned here: renaming the field breaks
// this test at COMPILE time, which is the loudest failure there is.
func TestEveryRefusalIsFindable(t *testing.T) {
	const reason = "classified DANGEROUS"
	cases := []struct {
		name   string
		answer any
	}{
		{"jmsx_exec", &jmsexec.CommandResult{Blocked: jms.Opt(reason)}},
		{"jmsx_run", &jmsexec.BatchResult{Blocked: jms.Opt(reason)}},
		{"jmsx_collect", &jmsexec.CollectResult{Blocked: jms.Opt(reason)}},
		{"jmsx_collect_multi", &jmsexec.CollectMultiResult{Blocked: jms.Opt(reason)}},
		{"jmsx_exec_multi", &jmsexec.ExecMultiResult{Blocked: jms.Opt(reason)}},
		{"jmsx_bg_start", &jmsexec.BgStartResult{Blocked: jms.Opt(reason)}},
	}
	for _, c := range cases {
		got := render.OutcomeOf(c.answer)
		if got == nil {
			t.Errorf("%s: the answer declares no verdict at all", c.name)
			continue
		}
		if got.Refused != reason {
			t.Errorf("%s: the refusal was not found: %q", c.name, got.Refused)
		}
		if !(got.Refused != "" && !got.DryRun) {
			t.Errorf("%s: the refusal does not count as stopped", c.name)
		}
		if got.Acted() {
			t.Errorf("%s: a refusal counts as something that acted", c.name)
		}
	}
}

// writeLine drops one line into the trail directory, in the trail's shape.
//
// ⚠️ The file name has to be one the reader RECOGNISES (the reader only
// reads files whose name has the trail's shape — otherwise a fixture is a
// fixture the reader ignores). The shape is the writer's, so the name is
// built the way the writer builds it.
func writeLine(t *testing.T, settings audit.Settings, line string) {
	t.Helper()
	path := filepath.Join(settings.Dir, audit.FileName())
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("write the trail line: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("write the trail line: %v", err)
	}
}

// The answer must SAY that it did not see everything.
//
// The promise of this tool is "the trail is the truth" — and a reader that
// silently dropped lines would break it. The count of the lines it could
// not take is in the CONTRACT (so an agent does not have to parse prose),
// and the note says it in words (so a person reading it sees it too).
func TestTheAnswerSaysWhenItCouldNotReadEverything(t *testing.T) {
	dir := t.TempDir()
	settings := audit.Settings{Mode: "file", Dir: dir}

	// A line longer than the reader's limit.
	long := `{"ts":"2026-10-28T09:00:01Z","service":"jumpserver-mcp-exec","tool":"jmsx_exec","args":"` +
		strings.Repeat("x", 2_000_000) + `"}`
	path := filepath.Join(dir, audit.FileName())
	if err := os.WriteFile(path, []byte(long+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cs := testsupport.NewSession(t, "audit-skipped-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterAuditTools(r, settings) })
	out := testsupport.CallToolJSON(t, cs, "jmsx_audit_query", map[string]any{})

	note, _ := out["note"].(string)
	if !strings.Contains(note, "行读不了") {
		t.Errorf("the note does not say that lines were skipped: %q", note)
	}
	if !strings.Contains(note, "1 行") {
		t.Errorf("the note does not say HOW MANY: %q", note)
	}
	skipped, ok := out["skipped"].(float64)
	if !ok {
		t.Fatalf("the answer carries no skipped count: %+v", out)
	}
	if skipped != 1 {
		t.Errorf("the answer says %v lines were skipped, want 1", skipped)
	}
}

// And when everything was read, the answer must NOT claim otherwise.
func TestTheAnswerDoesNotClaimItMissedAnything(t *testing.T) {
	dir := t.TempDir()
	settings := audit.Settings{Mode: "file", Dir: dir}
	writeLine(t, settings, `{"ts":"2026-10-28T09:00:00+08:00","service":"jumpserver-mcp-exec",`+
		`"tool":"jmsx_exec","allowed":true}`)

	cs := testsupport.NewSession(t, "audit-not-skipped-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterAuditTools(r, settings) })
	out := testsupport.CallToolJSON(t, cs, "jmsx_audit_query", map[string]any{})

	note, _ := out["note"].(string)
	if strings.Contains(note, "行读不了") {
		t.Errorf("the note claims lines were skipped when none were: %q", note)
	}
	// The limit in the note is the one that was IN FORCE (the default),
	// not the ceiling: "at most 500" when the call asked for 50 is a true
	// sentence about the wrong thing.
	if !strings.Contains(note, fmt.Sprintf("本次最多 %d 行", audit.DefaultReadLimit)) {
		t.Errorf("the note does not say which limit was in force: %q", note)
	}
	if skipped, ok := out["skipped"].(float64); ok && skipped != 0 {
		t.Errorf("the answer says %v lines were skipped, want 0", skipped)
	}
}

// The limit the answer names is the one that was ASKED FOR.
//
// ⚠️ The guard only covered the default: a reader that ignored the
// argument and always said "at most 50" stayed green — and the answer
// then names a limit that was never in force, which is a true sentence
// about the wrong thing.
func TestTheAnswerNamesTheLimitThatWasAskedFor(t *testing.T) {
	dir := t.TempDir()
	settings := audit.Settings{Mode: "file", Dir: dir}
	writeLine(t, settings, `{"ts":"2026-10-28T09:00:00+08:00","service":"jumpserver-mcp-exec",`+
		`"tool":"jmsx_exec","allowed":true}`)

	cs := testsupport.NewSession(t, "audit-limit-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterAuditTools(r, settings) })
	out := testsupport.CallToolJSON(t, cs, "jmsx_audit_query", map[string]any{"limit": 7})

	note, _ := out["note"].(string)
	if !strings.Contains(note, fmt.Sprintf("本次最多 %d 行", 7)) {
		t.Errorf("the note does not name the limit that was asked for: %q", note)
	}
}

// "truncated" has to mean what it says: the trail held more than came
// back.
//
// ⚠️ Two things have to stay apart: the lines that were CUT (the limit),
// and the lines that could not be READ (too long, or broken). The first is
// what truncated is about; the second is what skipped is about.
func TestTheAnswerSaysWhenItWasCutShort(t *testing.T) {
	dir := t.TempDir()
	settings := audit.Settings{Mode: "file", Dir: dir}
	for i := 0; i < 3; i++ {
		writeLine(t, settings, fmt.Sprintf(
			`{"ts":"2026-10-28T09:00:0%d+08:00","service":"jumpserver-mcp-exec","tool":"jmsx_exec","allowed":true}`, i))
	}

	cs := testsupport.NewSession(t, "audit-truncated-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterAuditTools(r, settings) })

	// Everything fits: not truncated.
	out := testsupport.CallToolJSON(t, cs, "jmsx_audit_query", map[string]any{})
	if truncated, _ := out["truncated"].(bool); truncated {
		t.Errorf("the answer says it was cut short, but it returned everything: %+v", out)
	}
	if total, _ := out["total"].(float64); total != 3 {
		t.Errorf("the answer says %v lines matched, want 3: %+v", total, out)
	}

	// The limit cuts it: truncated, and the count of what matched is still
	// the whole truth.
	out = testsupport.CallToolJSON(t, cs, "jmsx_audit_query", map[string]any{"limit": 1})
	if truncated, _ := out["truncated"].(bool); !truncated {
		t.Errorf("the answer does not say it was cut short, but it returned 1 of 3: %+v", out)
	}
	if total, _ := out["total"].(float64); total != 3 {
		t.Errorf("the answer says %v lines matched, want 3: the count of what matched is the whole truth", total)
	}
	if count, _ := out["count"].(float64); count != 1 {
		t.Errorf("the answer says it returned %v lines, want 1: %+v", count, out)
	}
}

// When the trail is OFF, the answer has to say so.
//
// ⚠️ Every number in that answer reads like "that is everything":
// count=0, total=0, truncated=false, skipped=0. The truth is that this
// machine kept NOTHING — and this tool exists for the case where a
// refused call left no trace anywhere.
func TestTheAnswerSaysWhenTheTrailIsOff(t *testing.T) {
	cs := testsupport.NewSession(t, "audit-off-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterAuditTools(r, audit.Settings{Mode: "off"}) })
	out := testsupport.CallToolJSON(t, cs, "jmsx_audit_query", map[string]any{})

	note, _ := out["note"].(string)
	if !strings.Contains(note, "关闭") {
		t.Errorf("the note does not say that the trail is off: %q", note)
	}
}

// The retention the answer promises must be the retention the pruner
// honours.
//
// ⚠️ The pruner CLAMPS the window (the multiplication overflows past a
// century), and a note that promises more than the machine keeps is a lie
// about the evidence.
func TestTheAnswerPromisesNoMoreThanThePrunerKeeps(t *testing.T) {
	note := retentionNote(200000)
	if strings.Contains(note, "200000") {
		t.Errorf("the answer promises a retention the pruner does not honour: %q", note)
	}
	if !strings.Contains(note, fmt.Sprintf("%d", audit.MaxRetentionDays)) {
		t.Errorf("the answer does not name the retention that is really in force: %q", note)
	}
}

// The answer has to say how far back the trail goes.
//
// ⚠️ Without it, "0 lines matched" is a FALSE silence: the files that held
// the answer may have been pruned by the retention policy, and an agent
// asking "what happened yesterday" would be told "nothing happened
// yesterday" — which is not the same fact as "the machine kept nothing".
func TestTheAnswerSaysHowFarBackTheTrailGoes(t *testing.T) {
	dir := t.TempDir()
	settings := audit.Settings{Mode: "file", Dir: dir, RetentionDays: 7}
	writeLine(t, settings, `{"ts":"2026-10-28T09:00:00+08:00","service":"jumpserver-mcp-exec",`+
		`"tool":"jmsx_exec","allowed":true}`)

	cs := testsupport.NewSession(t, "audit-retention-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterAuditTools(r, settings) })
	out := testsupport.CallToolJSON(t, cs, "jmsx_audit_query", map[string]any{})

	note, _ := out["note"].(string)
	if !strings.Contains(note, "7 天") {
		t.Errorf("the note does not say how far back the trail goes: %q", note)
	}
}
