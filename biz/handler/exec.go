package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/render"

	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/risk"
)

// The command tools: run a command on a managed machine through the
// bastion, and check whether the bastion can open a shell on it at all.
func RegisterExecTools(r *reg.Registry, cfg config.Config) {
	type execArgs struct {
		Asset   string `json:"asset" jsonschema:"asset name, address or partial name as shown in JumpServer, e.g. node-071 or 192.0.2.71"`
		Command string `json:"command" jsonschema:"shell command to run on the asset; runs as the asset account, NOT root -- use sudo -n yourself if root is needed"`
		Timeout int    `json:"timeout,omitempty" jsonschema:"seconds to wait (default 60, max 600)"`
		Full    bool   `json:"full,omitempty" jsonschema:"fetch the COMPLETE output even when it exceeds the terminal window (slower: costs extra round trips)"`
		DryRun  bool   `json:"dry_run,omitempty" jsonschema:"classify the command and report whether it would run, WITHOUT running it (no session, no side effect)"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true when the command is classified DANGEROUS (rm -rf, reboot, mkfs, privilege changes). A harmless command does not need it"`
	}

	reg.Add(r, &mcp.Tool{
		Name:         "jmsx_exec",
		Description:  "Run a shell command on a JumpServer-managed machine THROUGH THE BASTION (koko 2222), returning stdout+stderr and the exit code. Use it for one command on one machine; jmsx_run runs several commands in one session, jmsx_exec_multi runs one command on many machines. The session is recorded in JumpServer's audit trail. The command runs as the asset account and is not elevated: prefix with 'sudo -n' for root. The answer names the account it ran as. Output is normally complete; with the koko terminal backend it is bounded by the terminal window, and full=true then fetches the rest (costs extra round trips).",
		OutputSchema: render.Schema[jmsexec.CommandResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args execArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Asset) == "" || strings.TrimSpace(args.Command) == "" {
			return render.Failf("both asset and command are required")
		}
		// The command is classified before anything else happens, so a
		// destructive one is refused WITHOUT opening a session.
		//
		// The answer is a plan that was not carried out, not an error: the
		// caller reads it, adds confirm=true and goes on. This is the same
		// rule the management plane's jms_job_run applies.
		cmdClass := risk.Classify(args.Command)
		if args.DryRun {
			return render.Text(dryRunCommand(args.Asset, args.Command, cmdClass, args.Confirm))
		}
		if risk.RequiresConfirmation(cmdClass) && !args.Confirm {
			return render.Text(blockedCommand(args.Asset, args.Command, cmdClass.String()))
		}
		timeout := time.Duration(args.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		if timeout > 600*time.Second {
			timeout = 600 * time.Second
		}

		k, err := koko.Open(ctx, cfg.Koko, args.Asset, "", timeout+90*time.Second)
		if err != nil {
			return fail(err)
		}
		defer k.Close()

		out, code, truncated, err := k.Exec(args.Command, timeout)
		if err != nil {
			// ⚠️ The typed error, not its text. The error may know how far
			// the command got — not sent, or MAY ALREADY BE RUNNING on the
			// machine — and that is the one fact the caller cannot work
			// out for itself after a failure, and the one that decides
			// whether running it again is allowed.
			//
			// The agent reads the error's OWN text: the registry turns
			// the error back into an error result unchanged.
			//
			// ⚠️ Two texts DID change in this commit, on purpose: the
			// timeout's and the lost-reply's now say "it may already be
			// running on the machine", because an agent that does not
			// hear it retries — and retrying a command that may still be
			// running is how `rm -rf` runs twice. Every other path is
			// byte for byte what it was.
			return fail(err)
		}
		// The shape of the answer is the contract (idl/jmsexec/jmsexec.thrift),
		// not a map assembled here: the tool produces the contract, so the
		// wire format has one owner.
		res := &jmsexec.CommandResult{
			Asset:    args.Asset,
			Command:  args.Command,
			ExitCode: int32(code),
			Output:   out,
			Via:      k.Describe(),
		}
		// Which account the command ran as. An asset can carry several, and
		// the session picks one, so the answer says which.
		if acct := k.Account(); acct != "" {
			res.Account = jms.Opt(acct)
		}
		// Whether the session came from the connection pool. It is the
		// difference between a session that paid for a token and a
		// handshake and one that did not.
		res.Reused = jms.OptBool(k.Reused())
		// A session that went through the terminal instead of a connection
		// token paid about ten seconds for it. The reason belongs in the
		// answer: the fast path being unavailable is the operator's to fix,
		// and silently paying the slow price hides it.
		if reason := k.FallbackReason(); reason != "" {
			res.Warning = jms.Opt(fmt.Sprintf(
				"the connection-token backend was unavailable, so this session went through the koko "+
					"terminal instead, which costs about ten seconds more to open: %s", reason))
		}
		// -1 means the trailing marker never came back, i.e. the command killed
		// the session (a bare `exit`), or its output was not parsable. Say so
		// rather than letting the caller read -1 as a real status.
		if truncated {
			if args.Full {
				full, ferr := k.PullCommandOutput(args.Command, timeout)
				if ferr != nil {
					res.FullOutputError = jms.Opt(ferr.Error())
				} else {
					res.Output = full
					res.OutputBytes = jms.OptInt32(int32(len(full)))
					truncated = false
					res.Via = "jumpserver koko (bastion), recorded; full output re-read in chunks"
				}
			}
			if truncated {
				res.Truncated = jms.OptBool(true)
				res.Warning = jms.Opt("output was longer than the terminal window, so the top " +
					"is missing. Narrow the result (pipe through head/tail/grep) or re-run " +
					"with full=true, before drawing conclusions from it.")
			}
		}
		if code < 0 {
			res.Warning = jms.Opt("exit code unavailable (-1): the command most likely " +
				"terminated the shell (bare `exit`) or produced output the terminal " +
				"parser could not recover. Wrap it in a subshell, e.g. `sh -c '...'`.")
		}
		return render.Text(res)
	})

	type pingArgs struct {
		Asset string `json:"asset" jsonschema:"asset name or address to test"`
	}
	reg.Add(r, &mcp.Tool{
		Name:         "jmsx_reachable",
		Description:  "Check whether the bastion can open a shell on an asset (auth, permission and protocol all have to work). Use before a long job.",
		OutputSchema: render.Schema[jmsexec.ReachableResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args pingArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Asset) == "" {
			return render.Failf("asset is required")
		}
		res := &jmsexec.ReachableResult{Asset: args.Asset}
		k, err := koko.Open(ctx, cfg.Koko, args.Asset, "", 90*time.Second)
		if err != nil {
			res.Error = jms.Opt(err.Error())
			if d := reg.DeliveryOf(err); d != jms.Delivery(0) {
				res.Delivery = jms.Opt(d.String())
			}
			return render.Text(res)
		}
		defer k.Close()

		out, code, truncated, err := k.Exec("id -un; hostname", 30*time.Second)
		_ = truncated
		if err != nil {
			res.Error = jms.Opt(err.Error())
			if d := reg.DeliveryOf(err); d != jms.Delivery(0) {
				res.Delivery = jms.Opt(d.String())
			}
			return render.Text(res)
		}
		// An empty result with no exit code means the shell opened but the probe
		// produced nothing parsable (a router that accepts the connection but
		// never runs the command, or output the parser could not recover).
		// Reporting that as reachable would be a false positive.
		if code < 0 && strings.TrimSpace(out) == "" {
			res.Error = jms.Opt("opened a shell but the probe returned no parsable output " +
				"(exit code unavailable); the asset may not complete a session")
			return render.Text(res)
		}
		res.Reachable = true
		res.WhoamiHostname = jms.Opt(out)
		res.ExitCode = jms.OptInt32(int32(code))
		return render.Text(res)
	})
}
