package handler

// jmsx_run: several commands on one machine over one session —
// the per-machine counterpart of jmsx_exec.

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/render"
	"github.com/yi-nology/jumpserver-kit/pkg/risk"
)

// jmsx_run: several commands on one machine over one session —
// the per-machine counterpart of jmsx_exec.

func registerRunTool(r *reg.Registry, cfg config.Config) {

	type runArgs struct {
		Asset    string   `json:"asset" jsonschema:"asset name or address, e.g. node-071"`
		Commands []string `json:"commands" jsonschema:"commands to run in order on the same shell; far cheaper than one call per command"`
		Timeout  int      `json:"timeout,omitempty" jsonschema:"seconds allowed per command (default 60)"`
		DryRun   bool     `json:"dry_run,omitempty" jsonschema:"classify the commands and report whether they would run, WITHOUT running them (no session, no side effect)"`
		Confirm  bool     `json:"confirm,omitempty" jsonschema:"must be true when any of the commands is classified DANGEROUS (rm -rf, reboot, mkfs, privilege changes). Harmless commands do not need it"`
	}

	reg.Add(r, &mcp.Tool{
		Name: "jmsx_run",
		Description: "Run several commands on ONE machine through a single bastion session and return each result in order. " +
			"Much cheaper than calling jmsx_exec repeatedly: each exec re-walks the koko TUI (~10s), this pays it once. " +
			"Use it when you need several facts from the same host, or a short sequence that depends on earlier output. " +
			"Commands still run as the asset account and are fully recorded.",
		OutputSchema: render.Schema[jmsexec.BatchResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, any, error) {
		if len(args.Commands) == 0 {
			return render.Failf("commands is required and must not be empty")
		}
		// The worst command of the batch decides: one destructive command in
		// the list is enough to ask for the confirmation.
		cmdClass := jms.CommandClass_READ
		for _, c := range args.Commands {
			if cc := risk.Classify(c); cc == jms.CommandClass_DANGEROUS {
				cmdClass = cc
				break
			}
		}
		// One answer shape for all three outcomes: run, refused, rehearsed.
		// What differs is which fields are filled, not which fields exist —
		// a caller reads the same keys either way.
		res := &jmsexec.BatchResult{
			Asset:    args.Asset,
			Commands: args.Commands,
		}
		riskClass, would, blocked := rehearsal(cmdClass, args.Confirm)
		if args.DryRun {
			render.FillRehearsal(res, riskClass, would, blocked, render.RehearsalNote)
			return render.Text(res)
		}
		if !would {
			render.FillRefusal(res, riskClass, blocked)
			return render.Text(res)
		}
		timeout := commandTimeout(args.Timeout, 60)
		// Opening the session is paid once; each command then gets its own
		// budget on top of it.
		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, timeout+120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()

		results, runErr := s.Exec(args.Commands, timeout)
		res.Results = results
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
