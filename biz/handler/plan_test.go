package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-exec/internal/service/policy"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/testsupport"
)

// The rehearsal of a fleet sweep has to answer a second question, not just
// the first one: "is this command safe" is only half of what a caller wants
// to know before committing twenty machines to a sweep. The other half is
// "how long will this take, and in how many rounds".
//
// These tests pin the arithmetic of that answer. They assert relations
// rather than literal seconds, because the numbers behind the plan are
// MEASURED on the bastion (a walk is ~7.7s, a command ~1.4s) and a test
// that hardcoded them would break the first time the fleet got faster.
//
// The plan is read as the contract type, both straight out of the function
// and back from the wire: it is the same type in both places, so a field
// that exists in one but not the other is a bug the test would not see.

// planNumber reads a numeric field out of a plan.
func planNumber(t *testing.T, plan *jmsexec.SweepPlan, key string) float64 {
	t.Helper()
	switch key {
	case "hosts":
		return float64(plan.GetHosts())
	case "sessions":
		return float64(plan.GetSessions())
	case "commands_per_host":
		return float64(plan.GetCommandsPerHost())
	case "concurrency":
		return float64(plan.GetConcurrency())
	case "rounds":
		return float64(plan.GetRounds())
	case "seconds_per_host":
		return plan.GetSecondsPerHost()
	case "estimate_seconds":
		return plan.GetEstimateSeconds()
	case "min_seconds":
		return plan.GetMinSeconds()
	case "max_seconds":
		return plan.GetMaxSeconds()
	case "walk_seconds":
		return plan.GetWalkSeconds()
	case "command_seconds":
		return plan.GetCommandSeconds()
	default:
		t.Fatalf("planNumber does not know the field %q", key)
		return 0
	}
}

// fakeAssets is a list of asset references for the plan to price.
func fakeAssets(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("host-%02d", i))
	}
	return out
}

// TestPlanSweepPricesTwentyHosts is the case the plan exists for: twenty
// machines, three facts each, the default concurrency of four.
func TestPlanSweepPricesTwentyHosts(t *testing.T) {
	plan := planSweep(context.Background(), config.Config{}, fakeAssets(20), 3, 4, 60)

	if got := planNumber(t, plan, "hosts"); got != 20 {
		t.Errorf("hosts = %v, want 20", got)
	}
	// Twenty hosts at concurrency four is five waves of four.
	if got := planNumber(t, plan, "rounds"); got != 5 {
		t.Errorf("rounds = %v, want 5", got)
	}
	if got := planNumber(t, plan, "concurrency"); got != 4 {
		t.Errorf("concurrency = %v, want 4", got)
	}
	if got := planNumber(t, plan, "sessions"); got != 20 {
		t.Errorf("sessions = %v, want 20 (one recorded session per host)", got)
	}

	walk := planNumber(t, plan, "walk_seconds")
	cmd := planNumber(t, plan, "command_seconds")
	perHost := planNumber(t, plan, "seconds_per_host")

	if want := walk + 3*cmd; perHost != want {
		t.Errorf("seconds_per_host = %v, want %v (the walk plus three commands)", perHost, want)
	}
	if got := planNumber(t, plan, "estimate_seconds"); got != 5*perHost {
		t.Errorf("estimate_seconds = %v, want %v (five rounds)", got, 5*perHost)
	}
	// The floor is the walks and nothing else.
	if got := planNumber(t, plan, "min_seconds"); got != 5*walk {
		t.Errorf("min_seconds = %v, want %v (five walks, no command time)", got, 5*walk)
	}
	// The ceiling is every command of every host running into its timeout.
	if got := planNumber(t, plan, "max_seconds"); got != 5*(walk+3*60) {
		t.Errorf("max_seconds = %v, want %v (five rounds of three commands at the 60s timeout)",
			got, 5*(walk+3*60))
	}
	if planNumber(t, plan, "min_seconds") >= planNumber(t, plan, "estimate_seconds") {
		t.Error("the floor is not below the estimate")
	}
	if planNumber(t, plan, "estimate_seconds") >= planNumber(t, plan, "max_seconds") {
		t.Error("the estimate is not below the ceiling")
	}
}

// The knob is the point of the estimate: if more concurrency makes the sweep
// cheaper, the plan has to say what it buys.
func TestPlanSweepSaysWhatMoreConcurrencyBuys(t *testing.T) {
	plan := planSweep(context.Background(), config.Config{}, fakeAssets(20), 3, 4, 60)

	faster := plan.GetAtMaxConcurrency()
	if faster == nil {
		t.Fatalf("the plan does not mention the concurrency knob: %v", plan)
	}
	if got := float64(faster.GetRounds()); got != 3 {
		t.Errorf("at concurrency 8, rounds = %v, want 3 (twenty hosts over eight slots)", got)
	}
	perHost := planNumber(t, plan, "seconds_per_host")
	if got := faster.GetEstimateSeconds(); got != 3*perHost {
		t.Errorf("at concurrency 8, estimate = %v, want %v", got, 3*perHost)
	}
	if faster.GetEstimateSeconds() >= plan.GetEstimateSeconds() {
		t.Error("more concurrency did not make the estimate cheaper")
	}
}

// A sweep that already runs at the ceiling has nothing to gain, so it must
// not claim otherwise.
func TestPlanSweepSaysNothingToGainAtTheCeiling(t *testing.T) {
	plan := planSweep(context.Background(), config.Config{}, fakeAssets(20), 1, 8, 60)
	if plan.GetAtMaxConcurrency() != nil {
		t.Errorf("the plan suggests a concurrency that is already in use: %v", plan)
	}
}

// The plan has to quote the same numbers the run will use, so the defaults
// are clamped the same way in both.
func TestPlanSweepClampsConcurrencyLikeTheRun(t *testing.T) {
	cases := []struct {
		want, hosts, got int
	}{
		{want: 4, hosts: 20, got: 0},  // unset -> the default
		{want: 8, hosts: 20, got: 99}, // above the ceiling
		{want: 3, hosts: 3, got: 8},   // more slots than hosts
		{want: 8, hosts: 20, got: 8},  // the ceiling itself
	}
	for _, tc := range cases {
		if got := effectiveConcurrency(tc.got, tc.hosts); got != tc.want {
			t.Errorf("effectiveConcurrency(%d, %d) = %d, want %d", tc.got, tc.hosts, got, tc.want)
		}
	}
	if got := effectiveTimeout(0); got != 60 {
		t.Errorf("effectiveTimeout(0) = %d, want 60", got)
	}
	if got := effectiveTimeout(30); got != 30 {
		t.Errorf("effectiveTimeout(30) = %d, want 30", got)
	}
}

// A plan built on numbers nobody measured must say so, rather than pass a
// guess off as a measurement.
func TestPlanSweepSaysWhereItsNumbersCameFrom(t *testing.T) {
	plan := planSweep(context.Background(), config.Config{}, fakeAssets(4), 1, 4, 60)
	if plan.GetWalkSource() == "" {
		t.Fatal("the plan does not say where its walk cost came from")
	}
	if plan.GetCaveat() == "" {
		t.Error("the plan does not say what it does not know")
	}
	if plan.GetMeasureWith() == "" {
		t.Error("the plan does not say how to measure the numbers")
	}
}

// The plan has to be inside the dry-run answer, because the dry run is the
// only place the caller is told about it before the sweep starts.
//
// No bastion is needed: a dry run returns before a session is opened.
func TestCollectMultiDryRunCarriesThePlan(t *testing.T) {
	assets := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		assets = append(assets, "host-"+string(rune('a'+i)))
	}
	out := callTool(t, "jmsx_collect_multi", map[string]any{
		"assets": assets,
		"checks": []map[string]any{
			{"name": "disk", "command": "df -h / | tail -1"},
			{"name": "up", "command": "uptime"},
		},
		"dry_run": true,
	})

	if out["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", out["dry_run"])
	}
	if out["would_execute"] != true {
		t.Errorf("would_execute = %v, want true (both checks are harmless)", out["would_execute"])
	}
	plan := planFromWire(t, out)
	if got := planNumber(t, plan, "rounds"); got != 5 {
		t.Errorf("plan.rounds = %v, want 5", got)
	}
	if got := planNumber(t, plan, "hosts"); got != 20 {
		t.Errorf("plan.hosts = %v, want 20", got)
	}
	if got := planNumber(t, plan, "estimate_seconds"); got <= 0 {
		t.Errorf("plan.estimate_seconds = %v, want a positive estimate", got)
	}
	if plan.GetEstimate() == "" {
		t.Error("the plan has no human-readable estimate")
	}
}

// A blocked sweep is exactly the case where the caller wants to know the
// cost before deciding to confirm: the plan belongs there too.
func TestCollectMultiBlockedStillCarriesThePlan(t *testing.T) {
	out := callTool(t, "jmsx_collect_multi", map[string]any{
		"assets": []string{"host-a", "host-b", "host-c"},
		"checks": []map[string]any{{"name": "wipe", "command": "rm -rf /var/lib/x"}},
	})
	if out["blocked"] == nil {
		t.Fatalf("the destructive check was not blocked: %v", out)
	}
	plan := planFromWire(t, out)
	if got := planNumber(t, plan, "rounds"); got != 1 {
		t.Errorf("plan.rounds = %v, want 1 (three hosts at concurrency four)", got)
	}
}

// The sibling tool answers the same question in the same shape: one command
// per host, so the per-host cost is the walk plus a single command.
func TestExecMultiDryRunCarriesThePlan(t *testing.T) {
	out := callTool(t, "jmsx_exec_multi", map[string]any{
		"assets":  []string{"host-a", "host-b", "host-c", "host-d", "host-e"},
		"command": "uptime",
		"dry_run": true,
	})
	if out["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", out["dry_run"])
	}
	plan := planFromWire(t, out)
	if got := planNumber(t, plan, "rounds"); got != 2 {
		t.Errorf("plan.rounds = %v, want 2 (five hosts at concurrency four)", got)
	}
	if got := planNumber(t, plan, "commands_per_host"); got != 1 {
		t.Errorf("plan.commands_per_host = %v, want 1", got)
	}
}

// The dry run must not leave the caller guessing which hosts it would have
// swept: the answer names them, in the order they were given.
func TestExecMultiDryRunNamesTheHosts(t *testing.T) {
	out := callTool(t, "jmsx_exec_multi", map[string]any{
		"assets":  []string{"host-a", "host-b"},
		"command": "uptime",
		"dry_run": true,
	})
	hosts, _ := out["assets"].([]any)
	if len(hosts) != 2 {
		t.Fatalf("the dry run does not name the hosts: %v", out)
	}
	if hosts[0] != "host-a" || hosts[1] != "host-b" {
		t.Errorf("the hosts are not in the order they were given: %v", hosts)
	}
}

// planFromWire reads the plan out of a tool answer, as the contract type.
//
// It goes through the wire on purpose: the answer the caller receives is
// JSON, and reading it back as the contract type is the only way to be sure
// the two spellings agree.
func planFromWire(t *testing.T, out map[string]any) *jmsexec.SweepPlan {
	t.Helper()
	raw, ok := out["plan"]
	if !ok {
		t.Fatalf("the answer carries no plan: %v", out)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal the plan: %v", err)
	}
	var plan jmsexec.SweepPlan
	if err := json.Unmarshal(b, &plan); err != nil {
		t.Fatalf("unmarshal the plan: %v\n%s", err, b)
	}
	return &plan
}

// callTool drives one tool of the exec server over an in-memory transport.
//
// No bastion is involved: every tool called here answers before a session
// is opened, which is the whole point of a dry run.
//
// A tool whose answer does not match its own output schema fails the call
// right here — so every test that calls a tool is also a test of the
// contract.
func callTool(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	cs := testsupport.NewSession(t, "plan-test", reg.Options{Policy: policy.Table},
		func(r *reg.Registry) { RegisterTransferTools(r, mustLoad(t)) })
	return testsupport.CallToolJSON(t, cs, name, args)
}

// A wave's opening costs the most expensive open in it: the opens of a
// wave run in parallel, so one cold host makes the whole wave cost a cold
// open. A wave of pooled hosts is nearly free.
func TestSweepTimingChargesAColdWaveForOneColdHost(t *testing.T) {
	cost := koko.Cost{WalkSeconds: 1, PooledSeconds: 0.01, CommandSeconds: 0.5}

	est, floor, ceiling := sweepTiming(
		[]string{"a", "b", "c", "d"}, []bool{true, true, false, true}, 2, 4, 60, cost)
	if est != 1+2*0.5 {
		t.Errorf("estimate = %v, want the wave's cold open plus its commands", est)
	}
	if floor != 1 {
		t.Errorf("floor = %v, want the cold open alone", floor)
	}
	if ceiling != 1+2*60 {
		t.Errorf("ceiling = %v, want the cold open plus two commands at the timeout", ceiling)
	}

	// The same wave, all pooled: the opening is nearly free.
	est, floor, _ = sweepTiming(
		[]string{"a", "b", "c", "d"}, []bool{true, true, true, true}, 2, 4, 60, cost)
	if est != 0.01+2*0.5 {
		t.Errorf("estimate = %v, want the pooled open plus the commands", est)
	}
	if floor != 0.01 {
		t.Errorf("floor = %v, want the pooled open alone", floor)
	}
}

// More concurrency means fewer waves, and a wave is priced the same way at
// any width.
func TestSweepTimingPricesTheSameWaveAtAnyWidth(t *testing.T) {
	cost := koko.Cost{WalkSeconds: 1, PooledSeconds: 0.01, CommandSeconds: 0.5}
	assets := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	pooled := make([]bool, len(assets))

	oneWave, _, _ := sweepTiming(assets, pooled, 1, 8, 60, cost)
	twoWaves, _, _ := sweepTiming(assets, pooled, 1, 4, 60, cost)
	if oneWave >= twoWaves {
		t.Errorf("more concurrency did not make the sweep cheaper: %v vs %v", oneWave, twoWaves)
	}
}

// The per-command timeout answers to a ceiling, and the ceiling has to hold
// BEFORE the multiplication (#27): `want` is a 64-bit int, and
// `time.Duration(want) * time.Second` wraps — want=9223372037 came out
// NEGATIVE, which sailed past the caller's `timeout > 600s` clamp (a
// negative is not greater) and reached the polling loop as a deadline in the
// past; want=18446744074 wrapped onto a 290ms positive, which the clamp
// watched pass by. The audit trail then said "the command may already be
// running on the machine" about a command that had just been cut off.
func TestCommandTimeoutClampsBeforeTheMultiplication(t *testing.T) {
	cases := []struct {
		name string
		want int
		def  int
	}{
		{"unset uses the default", 0, 60},
		{"a negative ask is the unset case", -1, 60},
		{"an in-range ask passes through", 60, 60},
		{"the boundary itself is allowed", 600, 60},
		{"one over the ceiling clamps", 601, 60},
		{"a large ask clamps", 100000, 60},
		{"the wrap-to-negative point (measured 2026-11-10)", 9223372037, 60},
		{"one past the wrap point", 9223372038, 60},
		{"the wrap-to-tiny-positive point (measured 2026-11-10)", 18446744074, 60},
		{"int64 overflow territory", 9223372036854775807, 60},
		{"a default above the ceiling clamps too", 0, 3600},
	}
	for _, c := range cases {
		got := commandTimeout(c.want, c.def)
		if got <= 0 || got > maxCommandSeconds*time.Second {
			t.Errorf("%s: commandTimeout(%d, %d) = %v, want it in (0, %s]",
				c.name, c.want, c.def, got, maxCommandSeconds*time.Second)
		}
	}

	// The two spellings that must stay distinguishable: the default and the
	// ceiling are the contract's two ends.
	if got := commandTimeout(0, 60); got != 60*time.Second {
		t.Errorf("commandTimeout(0, 60) = %v, want 60s (the default)", got)
	}
	if got := commandTimeout(9223372037, 60); got != maxCommandSeconds*time.Second {
		t.Errorf("commandTimeout(9223372037, 60) = %v, want %s (the ceiling)",
			got, maxCommandSeconds*time.Second)
	}
}
