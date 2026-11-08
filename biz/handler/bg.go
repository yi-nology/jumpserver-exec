package handler

// The background tools: start a long-running command detached from the
// session (it survives the bastion closing the connection), and check
// whether it has finished.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oekit/pkg/shellx"
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/render"
	"github.com/yi-nology/jumpserver-kit/pkg/risk"
)

// The background tools: start a long-running command detached from the
// session (it survives the bastion closing the connection), and check
// whether it has finished.

func registerBgStartTool(r *reg.Registry, cfg config.Config) {

	type bgStartArgs struct {
		Asset   string `json:"asset" jsonschema:"asset name or address"`
		Command string `json:"command" jsonschema:"the long-running command to start"`
		Path    string `json:"path,omitempty" jsonschema:"where to write the output on the asset; defaults to a file under /tmp"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true when the command is classified DANGEROUS (rm -rf, reboot, mkfs, privilege changes). A harmless command does not need it"`
	}

	reg.Add(r, &mcp.Tool{
		Name: "jmsx_bg_start",
		Description: "Start a long-running command on an asset in the BACKGROUND and return immediately. " +
			"Use it for work that outlasts a tool call -- a deploy, a migration, a long test -- instead of " +
			"holding a session open for it. The command is detached from the session, so it survives the " +
			"bastion closing the connection. Poll it with jmsx_bg_check and read the output with jmsx_get_file.",
		OutputSchema: render.Schema[jmsexec.BgStartResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args bgStartArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Command) == "" {
			return render.Failf("command is required")
		}
		cmdClass := risk.Classify(args.Command)
		res := &jmsexec.BgStartResult{
			Asset:   jms.Opt(args.Asset),
			Command: jms.Opt(args.Command),
		}
		riskClass, would, blocked := rehearsal(cmdClass, args.Confirm)
		if !would {
			render.FillRefusal(res, riskClass, blocked)
			return render.Text(res)
		}
		path := strings.TrimSpace(args.Path)
		if path == "" {
			path = defaultBgOutputPath()
		}
		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, 120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()

		// setsid detaches the process from the session, so it is not killed
		// when the bastion closes the connection. nohup keeps it alive if the
		// shell goes away.
		//
		// The exit code goes to a marker file next to the output, so the
		// status check can tell "finished" from "still running". Without it
		// the check can only ever report "running": the output file exists
		// long before the job is done, and a file that merely exists says
		// nothing about whether the work finished.
		//
		// The command runs in an INNER shell and the marker is written by the
		// outer one. Appending `; echo $? > marker` to the user's command
		// instead would break on any command that calls `exit`, because that
		// ends the shell before the marker is written -- and then the job
		// looks unfinished forever.
		marker := path + ".rc"
		qp, qm := shellx.Quote(path), shellx.Quote(marker)
		// wrapper is a WHOLE command line (words and quotes inside), not a
		// single argument: nohup is meant to split it. shellquote:ok
		wrapper := `sh -c 'sh -c "$1"; echo $? > "$2"' _ ` + shellx.Quote(args.Command) + " " + qm
		start := fmt.Sprintf("rm -f %s %s; setsid nohup %s > %s 2>&1 < /dev/null & echo started",
			qp, qm, wrapper, qp) //shellquote:ok — wrapper is a whole command line; splitting is intended
		out, _, _, err := s.Run(start, 60*time.Second)
		if err != nil {
			return fail(err)
		}
		res.OutputPath = jms.Opt(path)
		res.ExitMarker = jms.Opt(marker)
		res.Status = jms.Opt(strings.TrimSpace(out))
		res.Next = jms.Opt("poll with jmsx_bg_check, read the output with jmsx_get_file")
		res.Note = jms.Opt("the process is detached, so the bastion session closing does not stop it")
		return render.Text(res)
	})

}

// defaultBgOutputPath is the output path bg_start uses when the caller does
// not name one. It is ONE function rather than an inline Sprintf, because it
// is the single place the /tmp write-point policy lives (the sweep asked for
// the write-point to be 收口): every default output lands here, so the note
// below — and any future change to where these files go — has one address to
// edit instead of one buried in a handler body.
//
// ⚠️ Why the files are NOT cleaned automatically anywhere (the #19 verdict,
// argued here so it does not get relitigated at each call site):
//
//   - The files live on the ASSET, not in this process. A TTL or a size cap
//     enforced here would mean a background sweep of the asset's /tmp — a
//     second scheduler with its own failure modes, running unattended writes
//     on managed machines, for a problem an explicit flag already solves.
//   - While the task RUNS the files are its live output: deleting them is
//     losing the log. Only a FINISHED state is safe to clean, and only the
//     caller knows it has read what it needed — so cleanup is the caller's
//     explicit decision (jmsx_bg_check cleanup=true), never this side's.
//   - What this side OWNS is making the garbage visible: the FINISHED answer
//     says the files are still there and how big they got, so "it leaked"
//     can never be the silent outcome.
//
// The UnixNano suffix means a repeated bg_start never collides — which is
// also why the files never clean up after themselves: the path is never
// reused. That is the trade this function states: collision-freedom here,
// explicit cleanup there.
func defaultBgOutputPath() string {
	return fmt.Sprintf("/tmp/jmsx-bg-%d.log", time.Now().UnixNano())
}

// bgCheckCommand builds the ONE shell line bg_check runs.
//
// It is a function of its own so the command's shape is testable without a
// bastion — and so the .rc suffix discipline has one owner. That discipline
// bit once: the suffix used to be appended BOTH inside the Quote and outside
// it in the format string, so the check tested `<path>.rc.rc` — a file that
// never exists — and every check forever answered RUNNING (measured shape:
// `'/tmp/jmsx-bg-1.log.rc'.rc`). The suffix lives INSIDE the Quote here, and
// the format string carries no second copy of it.
//
// cleanup=true adds, in the FINISHED branch only: read the exit code FIRST
// (cat before rm — the marker is the source of the exit code), then remove
// the output and the marker, then mark the removal. The RUNNING branch never
// removes anything: the files are the running task's live output.
func bgCheckCommand(path string, cleanup bool) string {
	qp := shellx.Quote(path)
	qrc := shellx.Quote(path + ".rc")
	if cleanup {
		return fmt.Sprintf("sz=$(wc -c < %s 2>/dev/null); "+
			"if [ -f %s ]; then echo FINISHED rc=$(cat %s); rm -f %s %s; echo %s; "+
			"else echo RUNNING; fi; echo SIZE=$sz",
			qp, qrc, qrc, qp, qrc, bgCleanedMark)
	}
	return fmt.Sprintf(
		"if [ -f %s ]; then echo FINISHED rc=$(cat %s); else echo RUNNING; fi; wc -c < %s 2>/dev/null",
		qrc, qrc, qp)
}

// bgCleanedMark marks, in the check's output, that the cleanup actually ran.
// It is a letter-prefixed marker so it cannot be confused with the exit code
// or the size, and it is a const so the parser and the builder cannot drift.
const bgCleanedMark = "JMSX_BG_CLEANED"

// parseBgCheckOutput reads the check's output: the state, the exit code (when
// finished), and whether the cleanup ran. A function of its own for the same
// reason the builder is: the two ends of one shell line are tested together,
// so a marker rename fails a test instead of silently unmooring the parser.
func parseBgCheckOutput(out string) (state, exitCode string, cleaned bool) {
	state = "running"
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "FINISHED"):
			state = "finished"
			if i := strings.Index(line, "rc="); i >= 0 {
				exitCode = strings.TrimSpace(line[i+3:])
			}
		case line == bgCleanedMark:
			cleaned = true
		}
	}
	return state, exitCode, cleaned
}

func registerBgCheckTool(r *reg.Registry, cfg config.Config) {

	type bgCheckArgs struct {
		Asset   string `json:"asset" jsonschema:"asset name or address"`
		Path    string `json:"path" jsonschema:"the output path returned by jmsx_bg_start"`
		Cleanup bool   `json:"cleanup,omitempty" jsonschema:"after a FINISHED check, also remove the output file and the exit marker from the asset (they can reach GBs for a verbose task). Refused while the task is still running: the files are its live output"`
	}

	reg.Add(r, &mcp.Tool{
		Name: "jmsx_bg_check",
		Description: "Check whether a background command started with jmsx_bg_start is still running, and whether " +
			"it has finished. Read the output itself with jmsx_get_file. The output and the exit-marker files stay " +
			"on the asset after the task finishes (they are not removed automatically): pass cleanup=true to have " +
			"this check remove them once it reports FINISHED — a FINISHED check without cleanup answers with a note " +
			"saying the files are still there.",
		OutputSchema: render.Schema[jmsexec.BgCheckResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args bgCheckArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Path) == "" {
			return render.Failf("path is required")
		}
		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, 120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()

		out, _, _, err := s.Run(bgCheckCommand(args.Path, args.Cleanup), 60*time.Second)
		if err != nil {
			return fail(err)
		}
		state, exitCode, cleaned := parseBgCheckOutput(out)
		res := &jmsexec.BgCheckResult{
			Asset: jms.Opt(args.Asset),
			Path:  jms.Opt(args.Path),
			State: jms.Opt(state),
		}
		if cleaned {
			res.Cleaned = jms.OptBool(true)
			res.Next = jms.Opt("nothing left to read: the output and the exit marker were removed")
			res.Note = jms.Opt("the output file and the exit marker were removed from the asset (cleanup=true, state=finished)")
		} else if state == "finished" {
			res.Next = jms.Opt("read the output with jmsx_get_file")
			// The garbage is named, with its size, so "the files leaked"
			// can never be the silent outcome (issue #19). The files are
			// NOT removed here: the caller may not have read the output
			// yet — cleanup is the caller's explicit second call.
			res.Note = jms.Opt(fmt.Sprintf(
				"the output (%s) and the exit marker are still on the asset at %s and %s — "+
					"poll again with cleanup=true to remove them once you have read the output",
				bgBytesHint(out), args.Path, args.Path+".rc"))
		} else {
			if args.Cleanup {
				res.Note = jms.Opt("not cleaned: the task is still running and the files are its live output")
			}
			res.Next = jms.Opt("poll again with jmsx_bg_check; read partial output with jmsx_get_file")
		}
		if exitCode != "" {
			res.ExitCode = jms.Opt(exitCode)
		}
		return render.Text(res)
	})

}

// bgBytesHint picks the output file's size out of the check's output (the
// `wc -c` line) for the still-there note. An unparsable size degrades to
// "unknown size" rather than to silence — the fact that files remain is the
// point, and a missing size must not take the note down with it.
func bgBytesHint(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "FINISHED") || strings.HasPrefix(line, "RUNNING") || line == bgCleanedMark {
			continue
		}
		if _, err := strconv.ParseInt(line, 10, 64); err == nil {
			return fmt.Sprintf("%s bytes so far", line)
		}
	}
	return "unknown size"
}
