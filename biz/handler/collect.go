package handler

// The fact-collecting tools: named checks on one machine (jmsx_collect),
// and the same checks swept across many (jmsx_collect_multi).

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oekit/pkg/slicex"
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/render"
	"github.com/yi-nology/jumpserver-kit/pkg/risk"
)

// The fact-collecting tools: named checks on one machine (jmsx_collect),
// and the same checks swept across many (jmsx_collect_multi).

// collectCheck is one fact to collect: a label the caller will recognise
// later, and the command that produces it.
//
// The command is the caller's own, which is why the collect tools are
// classified like command runners rather than like readers.
type collectCheck struct {
	Name    string `json:"name" jsonschema:"label for this fact, e.g. 'disk'"`
	Command string `json:"command" jsonschema:"the command that produces it"`
}

// sessionCostNote explains what the measured numbers are for.
//
// The wording follows the backend, because the two paths do not cost the
// same thing: a session opened with a connection token pays for a token and
// a handshake, and one opened from the pool pays for neither.

// checkList turns the caller's checks into the contract's shape.
//
// The checks are the caller's own, and they are echoed back in the answer
// (so the caller can line the facts up with what it asked for), which means
// they need a shape of their own.
func checkList(checks []collectCheck) []*jmsexec.Check {
	out := make([]*jmsexec.Check, 0, len(checks))
	for _, c := range checks {
		out = append(out, &jmsexec.Check{
			Name:    jms.Opt(c.Name),
			Command: jms.Opt(c.Command),
		})
	}
	return out
}

// checkCommands flattens the checks into the command list the session runs,
// in the same order — fact i answers check i.

// checkCommands flattens the checks into the command list the session runs,
// in the same order — fact i answers check i.
func checkCommands(checks []collectCheck) []string {
	return slicex.Map(checks, func(c collectCheck) string { return c.Command })
}

// factMap lines each check up with its output: fact i belongs to check i, and
// a run that stopped early leaves the tail without an output — kept as an
// absent output rather than filled with a placeholder, because "no output"
// and "empty output" are different facts.

// factMap lines each check up with its output: fact i belongs to check i, and
// a run that stopped early leaves the tail without an output — kept as an
// absent output rather than filled with a placeholder, because "no output"
// and "empty output" are different facts.
func factMap(checks []collectCheck, outputs []string) map[string]*jmsexec.Fact {
	facts := map[string]*jmsexec.Fact{}
	for i, c := range checks {
		f := &jmsexec.Fact{Command: jms.Opt(c.Command)}
		if i < len(outputs) {
			f.Output = jms.Opt(outputs[i])
		}
		facts[c.Name] = f
	}
	return facts
}

// sweepCounts aggregates a sweep's per-host rows: how many ran, how many
// failed, the first failure, and whether every failure said "never sent".
//
// A nil row is skipped — the caller counts it apart ("never attempted" is a
// different thing from "failed"). A row the caller's skip predicate drops
// (stop_on_error's "not attempted") is not a failure either: it never ran,
// and counting it as one would say a sweep half failed when it was stopped
// on purpose.

// collectClass is the risk of the commands the caller asked for.
//
// The checks are not the tool's own commands — the caller writes them — so
// this tool is a command runner in disguise, and it has to be judged like
// one. The worst command of the list decides.
func collectClass(checks []collectCheck) jms.CommandClass {
	worst := jms.CommandClass_READ
	for _, c := range checks {
		if cc := risk.Classify(c.Command); cc == jms.CommandClass_DANGEROUS {
			return cc
		}
	}
	return worst
}

// collectCheck is one fact to collect: a label the caller will recognise
// later, and the command that produces it.
//
// The command is the caller's own, which is why the collect tools are
// classified like command runners rather than like readers.

func registerCollectTool(r *reg.Registry, cfg config.Config) {

	type collectArgs struct {
		Asset   string         `json:"asset" jsonschema:"asset name or address"`
		Checks  []collectCheck `json:"checks" jsonschema:"the facts to collect, each with a name you will recognise later"`
		Timeout int            `json:"timeout,omitempty" jsonschema:"seconds per command (default 60)"`
		DryRun  bool           `json:"dry_run,omitempty" jsonschema:"classify the commands and report whether they would run, WITHOUT running them (no session, no side effect)"`
		Confirm bool           `json:"confirm,omitempty" jsonschema:"must be true when one of the commands is classified DANGEROUS (rm -rf, reboot, mkfs, privilege changes). Harmless commands do not need it"`
	}

	reg.Add(r, &mcp.Tool{
		Name: "jmsx_collect",
		Description: "Collect several named facts from ONE machine in a single bastion session and get them back " +
			"as a labelled map. Use it for a status sweep -- disk, memory, service state, versions -- where the " +
			"same facts are wanted on many machines: one call per machine instead of one per question. " +
			"jmsx_run does the same thing but returns a positional list, which is easy to misread when the " +
			"commands have different lengths.",
		OutputSchema: render.Schema[jmsexec.CollectResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args collectArgs) (*mcp.CallToolResult, any, error) {
		if len(args.Checks) == 0 {
			return render.Failf("checks is required and must not be empty")
		}
		// The checks are the caller's own commands, so this tool is judged
		// like a command runner: a destructive one is not executed without
		// the confirmation.
		class := collectClass(args.Checks)
		res := &jmsexec.CollectResult{
			Asset:  args.Asset,
			Checks: checkList(args.Checks),
		}
		riskClass, would, blocked := rehearsal(class, args.Confirm)
		if args.DryRun {
			render.FillRehearsal(res, riskClass, would, blocked, render.RehearsalNote)
			return render.Text(res)
		}
		if !would {
			render.FillRefusal(res, riskClass, blocked)
			return render.Text(res)
		}
		timeout := commandTimeout(args.Timeout, 60)
		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, timeout+120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()

		outputs, runErr := s.Exec(checkCommands(args.Checks), timeout)
		res.Facts = factMap(args.Checks, outputs)
		res.Stats = s.Stats()
		if runErr != nil {
			res.Partial = jms.OptBool(true)
			// How far the commands got. The trail asks "what may still be
			// running on that machine?", and this answer is where the
			// answer lives — the call itself succeeded, so nothing else
			// would say it.
			res.Error, res.Delivery = errFields(runErr)
		}
		return render.Text(res)
	})

}

func registerCollectMultiTool(r *reg.Registry, cfg config.Config) {

	type collectMultiArgs struct {
		Assets      []string       `json:"assets" jsonschema:"asset names or addresses to sweep"`
		Checks      []collectCheck `json:"checks" jsonschema:"the same facts to collect on every asset"`
		Timeout     int            `json:"timeout,omitempty" jsonschema:"seconds per command (default 60)"`
		Concurrency int            `json:"concurrency,omitempty" jsonschema:"how many hosts to sweep at once (default 4, max 8)"`
		DryRun      bool           `json:"dry_run,omitempty" jsonschema:"classify the commands and report whether they would run, WITHOUT running them (no session, no side effect)"`
		Confirm     bool           `json:"confirm,omitempty" jsonschema:"must be true when one of the commands is classified DANGEROUS (rm -rf, reboot, mkfs, privilege changes). Harmless commands do not need it"`
	}

	reg.Add(r, &mcp.Tool{
		Name:         "jmsx_collect_multi",
		Description:  "Collect the SAME set of facts from MANY machines and get back one matrix: a row per host, a column per fact. This is the fleet sweep -- disk, memory, service state, versions -- where the same questions are asked everywhere and the answers have to be lined up side by side. Each host gets ONE bastion session (so the facts of one host cannot mix with another's) and the hosts are swept in parallel. jmsx_exec_multi is the other way round: one command, many hosts. Pass dry_run=true first on a big sweep: it opens no session and reports how many rounds the sweep would take and roughly how many seconds it would cost, next to the risk classification.",
		OutputSchema: render.Schema[jmsexec.CollectMultiResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args collectMultiArgs) (*mcp.CallToolResult, any, error) {
		if len(args.Assets) == 0 {
			return render.Failf("assets is required and must not be empty")
		}
		if len(args.Checks) == 0 {
			return render.Failf("checks is required and must not be empty")
		}
		// The checks are the caller's own commands, so this tool is judged
		// like a command runner: a destructive one is not executed without
		// the confirmation.
		class := collectClass(args.Checks)
		// The plan and the run have to agree on the numbers, so the
		// defaults are resolved once and both use them. A plan that
		// quoted a different concurrency than the sweep would be a lie.
		timeout := time.Duration(effectiveTimeout(args.Timeout)) * time.Second
		concurrency := effectiveConcurrency(args.Concurrency, len(args.Assets))
		res := &jmsexec.CollectMultiResult{
			Assets:      args.Assets,
			Checks:      checkList(args.Checks),
			Concurrency: jms.OptInt32(int32(concurrency)),
		}
		riskClass, would, blocked := rehearsal(class, args.Confirm)
		if args.DryRun {
			render.FillRehearsal(res, riskClass, would, blocked, render.RehearsalNote)
			res.Plan = planSweep(ctx, cfg, args.Assets, len(args.Checks), concurrency, effectiveTimeout(args.Timeout))
			return render.Text(res)
		}
		if !would {
			render.FillRefusal(res, riskClass, blocked)
			res.Plan = planSweep(ctx, cfg, args.Assets, len(args.Checks), concurrency, effectiveTimeout(args.Timeout))
			return render.Text(res)
		}

		started := time.Now()
		// Whether any host's command may still be running there. The
		// call-level state can only be "maybe sent" — the other hosts ran
		// their commands, so "not sent" would be a lie.
		var maybeRunning atomic.Bool
		results := runSweep(args.Assets, concurrency, sweepOptions[*jmsexec.MatrixRow]{}, func(asset string) *jmsexec.MatrixRow {
			entry := &jmsexec.MatrixRow{Asset: jms.Opt(asset)}
			hostStarted := time.Now()
			s, err := koko.OpenSession(ctx, cfg.Koko, asset, timeout+180*time.Second)
			if err != nil {
				entry.Ok = jms.OptBool(false)
				// The session never opened, so the commands never left
				// this process. Saying so is what tells the operator
				// "this host never even started" apart from "this host
				// may still be running it".
				entry.Error, entry.Delivery = errFields(err)
				entry.Seconds = jms.OptFloat64(time.Since(hostStarted).Seconds())
				return entry
			}
			defer s.Close()
			// One session, all the checks: the facts of one host cannot mix
			// with another's, and the session is opened once per host.
			outputs, runErr := s.Exec(checkCommands(args.Checks), timeout)
			entry.Facts = factMap(args.Checks, outputs)
			entry.Ok = jms.OptBool(runErr == nil)
			if runErr != nil {
				entry.Error, entry.Delivery = errFields(runErr)
				if reg.DeliveryOf(runErr) == jms.Delivery_maybe_sent {
					maybeRunning.Store(true)
				}
			}
			entry.Seconds = jms.OptFloat64(time.Since(hostStarted).Seconds())
			return entry
		})

		matrix := map[string]*jmsexec.MatrixRow{}
		for _, r := range results {
			if r == nil {
				continue
			}
			// Every host goes into the matrix — the succeeded ones too.
			matrix[r.GetAsset()] = r
		}
		okCount, failCount, firstFailure, allNotSent := sweepCounts(results, nil)
		res.Matrix = matrix
		res.Delivery = callDelivery(maybeRunning.Load(), okCount, failCount, allNotSent)
		note := "one recorded bastion session per host; the facts of one host never mix with another's"
		if missing := len(args.Assets) - len(matrix); missing > 0 {
			// A host with no row at all is "nobody said", not "it did
			// not run" — and the caller has to be able to tell the
			// difference between "it failed" and "it never happened".
			note = fmt.Sprintf(
				"one recorded bastion session per host; %d host(s) have no row at all, "+
					"so nothing is known about them", missing)
		}
		if s := sweepSummary(okCount, failCount, firstFailure); s != "" {
			res.Error = jms.Opt(s)
		}
		res.Ok = jms.OptInt32(int32(okCount))
		res.Failed = jms.OptInt32(int32(failCount))
		res.Seconds = jms.OptFloat64(time.Since(started).Seconds())
		res.Note = jms.Opt(note)
		return render.Text(res)
	})

}
