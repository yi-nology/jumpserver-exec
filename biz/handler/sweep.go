package handler

// jmsx_exec_multi: one command across many machines, in parallel waves.
// The sweep machinery — the worker pool, the per-host aggregation, and
// the call-level delivery state — lives here.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oekit/concurrency/async"
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/render"
	"github.com/yi-nology/jumpserver-kit/pkg/risk"
)

// runSweep runs work on every asset with a bounded pool of workers and
// returns the rows in input order.
//
// The work for one asset is a whole session, and the assets are independent
// of each other, so the sessions overlap. That is where the speed comes
// from: establishing a koko session costs about ten seconds, and paying
// those one after another turns a ten-host sweep into two minutes of
// waiting. Rows keep the input order so the caller can line them up with
// the asset list without sorting.
//
// The two multi-host tools used to carry a private copy of this pool each,
// and the copies differed in more than taste — one could stop handing out
// work, the other could not, and neither said so. One pool with explicit
// options is one thing to reason about.
func runSweep[T any](assets []string, concurrency int, opts sweepOptions[T], work func(asset string) T) []T {
	results := make([]T, len(assets))
	jobs := make(chan int)
	var (
		wg      sync.WaitGroup
		stopped atomic.Bool
	)
	worker := func() {
		defer wg.Done()
		for idx := range jobs {
			asset := assets[idx]
			if opts.SkipBlank && strings.TrimSpace(asset) == "" {
				continue
			}
			row := work(asset)
			results[idx] = row
			if opts.StopAfter != nil && opts.StopAfter(row) {
				stopped.Store(true)
			}
		}
	}
	// async, not a bare goroutine: a panic in one host's work would
	// otherwise take the whole server down with it, and the other
	// hosts' results with it.
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		async.GoSafe(worker)
	}
feed:
	for idx := range assets {
		if stopped.Load() {
			results[idx] = opts.SkippedRow(assets[idx])
			continue
		}
		if opts.FeedBudget <= 0 {
			jobs <- idx
			continue
		}
		select {
		case jobs <- idx:
		case <-time.After(opts.FeedBudget):
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

// sweepOptions tunes runSweep beyond the plain "run everything" default.
//
// The zero value is the plain sweep: blanks go to work like any other
// name, nothing stops early, and handing out work waits as long as it
// takes.
type sweepOptions[T any] struct {
	// SkipBlank leaves a blank asset name WITHOUT a row — "never
	// attempted", not "failed". (Without it, a blank name is work like
	// any other and gets whatever row work produces for it.)
	SkipBlank bool
	// StopAfter is consulted after every finished row; once it says so,
	// no further asset is handed out and every remaining one gets
	// SkippedRow as its answer. nil means never stop.
	StopAfter func(row T) bool
	// SkippedRow builds the answer for an asset that was never attempted
	// because StopAfter said stop. Required when StopAfter is set.
	SkippedRow func(asset string) T
	// FeedBudget bounds how long handing out ONE asset may wait for a
	// free worker — a wedged worker must not be able to wedge the whole
	// call, because the other hosts' timeouts would never get to start.
	// The assets a broken feed leaves behind come back as zero rows,
	// which the caller reports as "never attempted". 0 waits forever.
	FeedBudget time.Duration
}

// sweepCounts aggregates a sweep's per-host rows: how many ran, how many
// failed, the first failure, and whether every failure said "never sent".
//
// A nil row is skipped — the caller counts it apart ("never attempted" is a
// different thing from "failed"). A row the caller's skip predicate drops
// (stop_on_error's "not attempted") is not a failure either: it never ran,
// and counting it as one would say a sweep half failed when it was stopped
// on purpose.
func sweepCounts[T interface {
	comparable
	GetOk() bool
	GetError() string
	GetDelivery() string
}](rows []T, skip func(T) bool) (ok, fail int, firstFailure string, allNotSent bool) {
	allNotSent = true
	for _, r := range rows {
		if any(r) == nil || (skip != nil && skip(r)) {
			continue
		}
		if r.GetOk() {
			ok++
			continue
		}
		fail++
		if firstFailure == "" {
			firstFailure = r.GetError()
		}
		if r.GetDelivery() != jms.Delivery_not_sent.String() {
			allNotSent = false
		}
	}
	return ok, fail, firstFailure, allNotSent
}

// callDelivery is the CALL-level delivery state of a sweep, from the
// per-host ones.
//
// The rule is the safe direction first: if ANY host may still be running
// its command, the sweep as a whole may still be running one — and the
// sweep must never be replayed. Only when nothing ran anywhere and every
// failure says "it never left" is the sweep itself safe to run again.
//
// A failure nobody classified ("nobody said") is not "not sent": it might
// be a command that is running right now, and saying otherwise is exactly
// the kind of mistake this whole column exists to prevent.
func callDelivery(maybeRunning bool, okCount, failCount int, allNotSent bool) *string {
	switch {
	case maybeRunning:
		return jms.Opt(jms.Delivery_maybe_sent.String())
	case failCount > 0 && okCount == 0 && allNotSent:
		// Nothing ran anywhere, and every failure says it never left:
		// running the whole sweep again is safe.
		return jms.Opt(jms.Delivery_not_sent.String())
	}
	return nil
}

// sweepSummary says what part of a sweep did not work, in one sentence.
//
// The per-host rows carry the detail; this is the CALL-level account, and
// the trail reads it — a sweep that half failed must not read in the trail
// like a sweep that worked (the rows are in the answer, and the trail
// does not read the rows).
func sweepSummary(ok, failed int, firstFailure string) string {
	if failed == 0 {
		return ""
	}
	if firstFailure == "" {
		firstFailure = "see the per-host rows"
	}
	return fmt.Sprintf("%d of %d hosts failed: %s", failed, ok+failed, firstFailure)
}

func registerExecMultiTool(r *reg.Registry, cfg config.Config) {

	type multiArgs struct {
		Assets      []string `json:"assets" jsonschema:"asset names or addresses to run the command on"`
		Command     string   `json:"command" jsonschema:"the shell command to run on every asset"`
		Timeout     int      `json:"timeout,omitempty" jsonschema:"seconds allowed per asset (default 60)"`
		StopOnError bool     `json:"stop_on_error,omitempty" jsonschema:"stop handing out further assets after the first failure instead of trying the rest"`
		Concurrency int      `json:"concurrency,omitempty" jsonschema:"how many hosts to run on at once (default 4, max 8). Every host gets its own bastion session, so this is what makes a fleet sweep fast."`
		DryRun      bool     `json:"dry_run,omitempty" jsonschema:"classify the command, report what the sweep would cost (rounds, estimated seconds) and whether it would run, WITHOUT running it (no session, no side effect)"`
		Confirm     bool     `json:"confirm,omitempty" jsonschema:"must be true when the command is classified DANGEROUS (rm -rf, reboot, mkfs, privilege changes). A harmless command does not need it"`
	}

	reg.Add(r, &mcp.Tool{
		Name:         "jmsx_exec_multi",
		Description:  "Run the SAME command on several machines through the bastion and return each host's output. Use it when a fact has to be collected from a fleet (disk usage, a version, whether a service is up) and the answer must be per host: jms_job_run fans out too, but the v5 API exposes no execution output for a job, so this is the multi-host call that actually returns results. jmsx_collect_multi is the other way round: different facts per host. Hosts run in parallel, each in its own recorded bastion session, so ten hosts cost roughly two rounds instead of ten. A host that cannot be reached is reported as failed rather than aborting the run. Pass dry_run=true first on a big sweep: it opens no session and reports how many rounds the sweep would take and roughly how many seconds it would cost, next to the risk classification.",
		OutputSchema: render.Schema[jmsexec.ExecMultiResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args multiArgs) (*mcp.CallToolResult, any, error) {
		if len(args.Assets) == 0 {
			return render.Failf("assets is required and must not be empty")
		}
		if strings.TrimSpace(args.Command) == "" {
			return render.Failf("command is required")
		}
		cmdClass := risk.Classify(args.Command)
		// The plan and the run have to agree on the numbers, so the
		// defaults are resolved once and both use them.
		timeout := time.Duration(effectiveTimeout(args.Timeout)) * time.Second
		concurrency := effectiveConcurrency(args.Concurrency, len(args.Assets))
		res := &jmsexec.ExecMultiResult{
			Assets:      args.Assets,
			Command:     jms.Opt(args.Command),
			Concurrency: jms.OptInt32(int32(concurrency)),
		}
		riskClass, would, blocked := rehearsal(cmdClass, args.Confirm)
		if args.DryRun {
			render.FillRehearsal(res, riskClass, would, blocked, render.RehearsalNote)
			res.Plan = planSweep(ctx, cfg, args.Assets, 1, concurrency, effectiveTimeout(args.Timeout))
			return render.Text(res)
		}
		if !would {
			render.FillRefusal(res, riskClass, blocked)
			res.Plan = planSweep(ctx, cfg, args.Assets, 1, concurrency, effectiveTimeout(args.Timeout))
			return render.Text(res)
		}

		// Every host gets its own session, so the hosts are independent and can
		// run at the same time. Opening a session used to cost about ten
		// seconds per host and was paid one host after another: a ten-host
		// sweep took two minutes for work that takes seconds once the
		// openings overlap.
		started := time.Now()
		// Whether any host's command may still be running there. The
		// call-level state can only be "maybe sent" — the other hosts
		// ran their commands, so "not sent" would be a lie.
		var maybeRunning atomic.Bool
		results := runSweep(args.Assets, concurrency, sweepOptions[*jmsexec.HostResult]{
			SkipBlank: true,
			StopAfter: func(row *jmsexec.HostResult) bool { return !row.GetOk() && args.StopOnError },
			SkippedRow: func(asset string) *jmsexec.HostResult {
				return &jmsexec.HostResult{
					Asset:   jms.Opt(asset),
					Ok:      jms.OptBool(false),
					Skipped: jms.OptBool(true),
					Error:   jms.Opt("not attempted: stop_on_error was set and an earlier host failed"),
					// It was never attempted, so nothing was sent. Saying
					// so keeps the column uniform: every host that did
					// not run says so.
					Delivery: jms.Opt(jms.Delivery_not_sent.String()),
				}
			},
			FeedBudget: timeout + 180*time.Second,
		}, func(asset string) *jmsexec.HostResult {
			entry := &jmsexec.HostResult{Asset: jms.Opt(asset)}
			hostStarted := time.Now()
			s, err := koko.OpenSession(ctx, cfg.Koko, asset, timeout+120*time.Second)
			if err != nil {
				entry.Ok = jms.OptBool(false)
				// The session never opened, so the command never
				// left this process.
				entry.Error, entry.Delivery = errFields(err)
				entry.Seconds = jms.OptFloat64(time.Since(hostStarted).Seconds())
				return entry
			}
			defer s.Close()
			out, code, truncated, runErr := s.Run(args.Command, timeout)
			entry.ExitCode = jms.OptInt32(int32(code))
			entry.Output = jms.Opt(out)
			entry.Seconds = jms.OptFloat64(time.Since(hostStarted).Seconds())
			// Whether this host's session came from the pool. It is the
			// difference between a host that paid for a token and a
			// handshake and one that did not, and the seconds above
			// only make sense next to it.
			entry.Reused = jms.OptBool(s.Reused())
			if truncated {
				entry.Truncated = jms.OptBool(true)
				entry.Warning = jms.Opt("output exceeded the terminal window, so the top is missing; " +
					"narrow it (pipe through head/tail/grep) or run the command on that host alone")
			}
			if runErr != nil {
				entry.Ok = jms.OptBool(false)
				entry.Error, entry.Delivery = errFields(runErr)
				if reg.DeliveryOf(runErr) == jms.Delivery_maybe_sent {
					maybeRunning.Store(true)
				}
			} else {
				entry.Ok = jms.OptBool(true)
			}
			return entry
		})

		skipped := 0
		ordered := []*jmsexec.HostResult{}
		notAttempted := 0
		for _, r := range results {
			if r == nil {
				// The asset was never handed to a worker. There is no
				// row for it at all, so the note has to say so — a
				// missing row is "nobody said", not "it did not run".
				//
				// Two things can cause it: the feed gave up before
				// handing it over, or its asset name was blank (the
				// worker skips those). They are counted apart, because
				// only one of them is the feed's doing.
				notAttempted++
				continue
			}
			ordered = append(ordered, r)
			if r.GetSkipped() {
				skipped++
			}
		}
		okCount, failCount, firstFailure, allNotSent := sweepCounts(results, func(r *jmsexec.HostResult) bool { return r.GetSkipped() })
		res.Delivery = callDelivery(maybeRunning.Load(), okCount, failCount, allNotSent)
		if s := sweepSummary(okCount, failCount, firstFailure); s != "" {
			res.Error = jms.Opt(s)
		}
		res.Hosted = jms.OptInt32(int32(len(ordered)))
		res.Ok = jms.OptInt32(int32(okCount))
		res.Failed = jms.OptInt32(int32(failCount))
		res.Skipped = jms.OptInt32(int32(skipped))
		res.Seconds = jms.OptFloat64(time.Since(started).Seconds())
		res.Results = ordered
		note := "every host ran in its own recorded bastion session"
		if notAttempted > 0 {
			note = fmt.Sprintf("%s; %d host(s) were never attempted, so nothing "+
				"is known about them", note, notAttempted)
		}
		res.Note = jms.Opt(note)
		return render.Text(res)
	})

}
