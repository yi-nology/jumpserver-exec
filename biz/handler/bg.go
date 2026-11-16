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

func registerBgStartTool(r *reg.Registry, cfg config.Config, mem approvalMemory) {

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
		riskClass, would, blocked, approvalNote := rehearsalM(mem, args.Asset, args.Command, cmdClass, args.Confirm, false)
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
		//
		// Three marker files ride beside the output (the .rc/.pid/.started
		// suffix discipline has ONE owner, bgStartCommand, and its parser
		// siblings -- the .rc.rc double-append bug once made every check
		// answer RUNNING forever):
		//
		//   - <path>.rc     exit code, written by the outer shell (above).
		//   - <path>.pid    the wrapper shell's own pid. setsid makes that
		//                   pid the process GROUP leader (pgid == pid), so
		//                   jmsx_bg_cancel can `kill -TERM -<pid>` the whole
		//                   group -- the inner `sh -c "$1"` and everything it
		//                   spawned die together. The wrapper writes `$$`
		//                   BEFORE exec'ing the inner shell, so the marker is
		//                   on disk before any caller could ask to cancel.
		//   - <path>.started  the start timestamp (date +%s), the clock the
		//                   bg_check TIMEOUT state (JMS_BG_TTL) reads.
		//
		// ⚠️ pid-reuse tradeoff, stated once (kael took the same one): between
		// the job exiting and the .pid being stale, the OS may recycle the
		// pid -- a cancel then TERM-signals an UNRELATED process group. The
		// window is tiny and the blast radius is bounded by the same-account
		// boundary; the alternative (kernel pidfd) is not available through a
		// shell one-liner. The cancel path mitigates it by checking the job's
		// OWN liveness evidence first.
		start := bgStartCommand(path, args.Command)
		out, _, _, err := s.Run(start, 60*time.Second)
		if err != nil {
			return fail(err)
		}
		res.OutputPath = jms.Opt(path)
		res.ExitMarker = jms.Opt(path + ".rc")
		res.PidMarker = jms.Opt(path + ".pid")
		res.Status = jms.Opt(strings.TrimSpace(out))
		res.Next = jms.Opt("poll with jmsx_bg_check, read the output with jmsx_get_file")
		res.Note = jms.Opt("the process is detached, so the bastion session closing does not stop it; cancel it with jmsx_bg_cancel (needs confirm=true)")
		// Why the gate let a dangerous command through: appended to the
		// note so the trail sees both facts in one place.
		if approvalNote != "" {
			res.Note = jms.Opt(res.GetNote() + "; " + approvalNote)
		}
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

// bgStartCommand builds the ONE shell line bg_start runs. It is a function of
// its own for the same reason bgCheckCommand is: the line's shape is testable
// without a bastion, and every marker suffix (.rc/.pid/.started/.cancelled)
// has exactly one writer here. The rm -f list clears ALL four markers so a
// re-run at the same path never inherits a stale verdict from a dead job.
//
// ⚠️ `date +%s > .started` lands BEFORE setsid: the timeout clock starts when
// the line runs, not when the wrapper happens to schedule.
func bgStartCommand(path, command string) string {
	qp := shellx.Quote(path)
	qrc := shellx.Quote(path + ".rc")
	qpid := shellx.Quote(path + ".pid")
	qstarted := shellx.Quote(path + ".started")
	qcancelled := shellx.Quote(path + ".cancelled")
	// The wrapper writes $$ (its own pid — the group leader after setsid) to
	// the pid file, then runs the command in an INNER shell and writes its
	// exit code to the rc file. wrapper is a WHOLE command line (words and
	// quotes inside), not a single argument: nohup is meant to split it.
	// shellquote:ok
	wrapper := `sh -c 'echo $$ > "$3"; sh -c "$1"; echo $? > "$2"' _ ` +
		shellx.Quote(command) + " " + qrc + " " + qpid
	return fmt.Sprintf("rm -f %s %s %s %s %s; date +%%s > %s; setsid nohup %s > %s 2>&1 < /dev/null & echo started",
		qp, qrc, qpid, qstarted, qcancelled, qstarted, wrapper, qp) //shellquote:ok — wrapper is a whole command line; splitting is intended
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
// The states are judged in priority order (kael five-state):
//
//   - .cancelled FIRST: a cancel that landed beats the exit-code file — the
//     job did not "succeed", it was killed (rc=143 was recorded into .rc, so
//     without this branch a cancelled job would masquerade as error).
//   - .rc: finished; the PARSER maps the recorded code to success/error.
//   - TIMEOUT (only when ttl > 0): no .rc, but the .started timestamp is
//     older than the TTL — still running past its bound. ttl == 0 omits the
//     branch entirely: the default deployment has no timeout state.
//   - otherwise RUNNING.
//
// cleanup=true adds, in the FINISHED branch only: read the exit code FIRST
// (cat before rm — the marker is the source of the exit code), then remove
// the output and the marker, then mark the removal. The RUNNING (and
// cancelled/timeout) branch never removes anything: the files are the
// running task's live output.
func bgCheckCommand(path string, cleanup bool, ttl time.Duration) string {
	qp := shellx.Quote(path)
	qrc := shellx.Quote(path + ".rc")
	qcancelled := shellx.Quote(path + ".cancelled")
	// timeoutClause is empty unless a TTL is configured: with ttl == 0 the
	// check has no timeout state, and the test pins the branch's absence.
	timeoutClause := ""
	if ttl > 0 {
		timeoutClause = fmt.Sprintf("elif [ -f %s ] && [ $(( $(date +%%s) - $(cat %s) )) -gt %d ]; then echo TIMEOUT; ",
			shellx.Quote(path+".started"), shellx.Quote(path+".started"), int64(ttl/time.Second))
	}
	if cleanup {
		return fmt.Sprintf("sz=$(wc -c < %s 2>/dev/null); "+
			"if [ -f %s ]; then echo CANCELLED at=$(cat %s); "+
			"elif [ -f %s ]; then echo FINISHED rc=$(cat %s); rm -f %s %s; echo %s; "+
			"%selse echo RUNNING; fi; echo SIZE=$sz",
			qp, qcancelled, qcancelled, qrc, qrc, qp, qrc, bgCleanedMark, timeoutClause)
	}
	return fmt.Sprintf(
		"if [ -f %s ]; then echo CANCELLED at=$(cat %s); "+
			"elif [ -f %s ]; then echo FINISHED rc=$(cat %s); "+
			"%selse echo RUNNING; fi; wc -c < %s 2>/dev/null",
		qcancelled, qcancelled, qrc, qrc, timeoutClause, qp)
}

// bgCleanedMark marks, in the check's output, that the cleanup actually ran.
// It is a letter-prefixed marker so it cannot be confused with the exit code
// or the size, and it is a const so the parser and the builder cannot drift.
const bgCleanedMark = "JMSX_BG_CLEANED"

// parseBgCheckOutput reads the check's output: the state, the exit code (when
// finished), and whether the cleanup ran. A function of its own for the same
// reason the builder is: the two ends of one shell line are tested together,
// so a marker rename fails a test instead of silently unmooring the parser.
//
// The five-state vocabulary (kael): FINISHED maps to success/error by the
// recorded exit code; CANCELLED/TIMEOUT/RUNNING are their own states.
// CANCELLED wins over FINISHED when both appear (the builder orders the
// branches so both should not appear, but the parser takes the stronger
// verdict anyway — a cancel is a cancel).
func parseBgCheckOutput(out string) (state, exitCode string, cleaned bool) {
	state = "running"
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "CANCELLED"):
			state = "cancelled"
		case strings.HasPrefix(line, "FINISHED"):
			if i := strings.Index(line, "rc="); i >= 0 {
				exitCode = strings.TrimSpace(line[i+3:])
			}
			if exitCode == "0" {
				state = "success"
			} else {
				state = "error"
			}
		case line == "TIMEOUT":
			state = "timeout"
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
		Cleanup bool   `json:"cleanup,omitempty" jsonschema:"after a FINISHED check, also remove the output file and exit marker from the asset (they can reach GBs for a verbose task). Needs confirm=true (it deletes files on the machine). Refused while the task is still running: the files are its live output"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true when cleanup is true (cleanup deletes files on the machine). A plain status check does not need it"`
	}

	reg.Add(r, &mcp.Tool{
		Name: "jmsx_bg_check",
		Description: "Check whether a background command started with jmsx_bg_start is still running, and whether " +
			"it has finished. The state is one of running / success / error / cancelled / timeout (the timeout state " +
			"appears only when JMS_BG_TTL is set and the job outlives it). Read the output itself with jmsx_get_file. " +
			"The output and the exit-marker files stay " +
			"on the asset after the task finishes (they are not removed automatically): pass cleanup=true to have " +
			"this check remove them once it reports a finished state — a finished check without cleanup answers with a note " +
			"saying the files are still there.",
		OutputSchema: render.Schema[jmsexec.BgCheckResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args bgCheckArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Path) == "" {
			return render.Failf("path is required")
		}
		// cleanup 在机器上删文件，是写不是读：没带 confirm 就拒——拒在开
		// 会话之前（零副作用），带 risk/blocked，与命令工具的拒绝同词汇。
		// 这是 verifier 抓的洞：cleanup 曾被当成 read 放行（只读模式
		// 拦不住、留痕记成 read——一场静默扩权）。
		if args.Cleanup && !args.Confirm {
			res := &jmsexec.BgCheckResult{
				Asset: jms.Opt(args.Asset),
				Path:  jms.Opt(args.Path),
				State: jms.Opt("unknown"),
			}
			render.FillRefusal(res, jms.Risk_write.String(),
				"set confirm=true to remove the finished job's output and exit marker (cleanup=true writes on the machine)")
			return render.Text(res)
		}
		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, 120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()

		out, _, _, err := s.Run(bgCheckCommand(args.Path, args.Cleanup, cfg.Koko.BgTTL), 60*time.Second)
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
			res.Note = jms.Opt(fmt.Sprintf(
				"the output file and the exit marker were removed from the asset (cleanup=true, state=%s)", state))
		} else if state == "cancelled" {
			res.ExitCode = jms.Opt("143")
			res.Next = jms.Opt("read the output with jmsx_get_file; the files are still on the asset")
			res.Note = jms.Opt("killed by jmsx_bg_cancel (rc=143 recorded)")
		} else if state == "timeout" {
			res.Next = jms.Opt("the files are its live output: read them with jmsx_get_file, or stop it with jmsx_bg_cancel (needs confirm=true)")
			res.Note = jms.Opt(fmt.Sprintf(
				"still running past JMS_BG_TTL=%s: the files are its live output; consider jmsx_bg_cancel",
				cfg.Koko.BgTTL))
		} else if state == "success" || state == "error" {
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
		if line == "" || strings.HasPrefix(line, "FINISHED") || strings.HasPrefix(line, "RUNNING") ||
			strings.HasPrefix(line, "CANCELLED") || line == "TIMEOUT" || line == bgCleanedMark {
			continue
		}
		if _, err := strconv.ParseInt(line, 10, 64); err == nil {
			return fmt.Sprintf("%s bytes so far", line)
		}
	}
	return "unknown size"
}

// bgCancelCommand builds the ONE shell line jmsx_bg_cancel runs. The branches,
// in priority order, and the parser below read them — the two ends of one
// shell line are tested together, like every builder/parser pair here.
//
//   - .rc exists → ALREADY: the job finished long ago; its recorded exit code
//     is reported and NOTHING is written (the .rc is history, not state).
//   - no .pid → NOMARK: the job predates pid markers and cannot be cancelled.
//   - otherwise → `kill -TERM -$pid`: the MINUS makes it a process-group
//     signal, and setsid (bg_start) guarantees pgid == pid, so the wrapper
//     shell, the inner `sh -c "$1"`, and everything the command spawned die
//     together. 25 × 0.2s gives the TERM 5s to land; still alive after that is
//     STILLRUNNING — alive, not dead, and the answer says exactly that.
//     Dead ⇒ record rc=143 into .rc (the check's five-state mapping turns the
//     pair .cancelled-first + rc into "cancelled", not "error") and stamp the
//     .cancelled file the check reads first.
//
// ⚠️ pid-reuse tradeoff (stated at bgStartCommand too): between the job
// exiting naturally and this line running, the OS may recycle the pid, and
// the TERM would land on an unrelated process group. The window is tiny and
// same-account bounded; there is no pidfd through a shell one-liner. What
// this line DOES do: check the job's own .rc first, and never write any
// marker when the target is still alive.
func bgCancelCommand(path string) string {
	qrc := shellx.Quote(path + ".rc")
	qpid := shellx.Quote(path + ".pid")
	qcancelled := shellx.Quote(path + ".cancelled")
	return fmt.Sprintf("if [ -f %s ]; then echo ALREADY rc=$(cat %s); "+
		"elif [ ! -f %s ]; then echo NOMARK; "+
		"else pid=$(cat %s); kill -TERM -$pid 2>/dev/null; i=0; "+
		"while kill -0 $pid 2>/dev/null && [ $i -lt 25 ]; do sleep 0.2; i=$((i+1)); done; "+
		"if kill -0 $pid 2>/dev/null; then echo STILLRUNNING; "+
		"else echo 143 > %s; echo $(date +%%s) > %s; echo CANCELLED; fi; fi",
		qrc, qrc, qpid, qpid, qrc, qcancelled)
}

// parseBgCancelOutput reads the cancel line's output: which of the four
// states the machine reported, and the recorded exit code when the job had
// already finished.
func parseBgCancelOutput(out string) (state, exitCode string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "CANCELLED"):
			state = "cancelled"
		case strings.HasPrefix(line, "ALREADY"):
			state = "already_finished"
			if i := strings.Index(line, "rc="); i >= 0 {
				exitCode = strings.TrimSpace(line[i+3:])
			}
		case line == "NOMARK":
			state = "no_marker"
		case line == "STILLRUNNING":
			state = "still_running"
		}
	}
	return state, exitCode
}

func registerBgCancelTool(r *reg.Registry, cfg config.Config) {

	type bgCancelArgs struct {
		Asset   string `json:"asset" jsonschema:"asset name or address"`
		Path    string `json:"path" jsonschema:"the output path returned by jmsx_bg_start"`
		Confirm bool   `json:"confirm" jsonschema:"must be true — this kills the background job's whole process group on the machine"`
	}

	reg.Add(r, &mcp.Tool{
		Name: "jmsx_bg_cancel",
		Description: "Cancel a background command started with jmsx_bg_start: send SIGTERM to its whole process " +
			"group (the wrapper shell, the command, and everything it spawned), wait up to 5s for it to exit, and " +
			"record rc=143. The output file is left in place — read it with jmsx_get_file. " +
			"Always requires confirm=true. If the job already finished, nothing is touched and its exit code is " +
			"reported; if it was started before pid markers existed, it cannot be cancelled.",
		OutputSchema: render.Schema[jmsexec.BgCancelResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args bgCancelArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Path) == "" {
			return render.Failf("path is required")
		}
		// 杀进程组是机器上的写：没带 confirm 就拒——拒在开会话之前（零副作用），
		// 与 bg_check cleanup 分支同一拒答模式（risk/blocked，与命令工具的
		// 拒绝同词汇）。
		if !args.Confirm {
			res := &jmsexec.BgCancelResult{
				Asset: jms.Opt(args.Asset),
				Path:  jms.Opt(args.Path),
				State: jms.Opt("unknown"),
			}
			render.FillRefusal(res, jms.Risk_write.String(),
				"set confirm=true to kill the background job on the machine")
			return render.Text(res)
		}
		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, 120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()

		out, _, _, err := s.Run(bgCancelCommand(args.Path), 60*time.Second)
		if err != nil {
			return fail(err)
		}
		state, exitCode := parseBgCancelOutput(out)
		res := &jmsexec.BgCancelResult{
			Asset: jms.Opt(args.Asset),
			Path:  jms.Opt(args.Path),
			State: jms.Opt(state),
		}
		switch state {
		case "cancelled":
			res.ExitCode = jms.Opt("143")
			res.Note = jms.Opt("sent SIGTERM to the process group, it exited (rc=143 recorded); output file left in place")
		case "already_finished":
			if exitCode != "" {
				res.ExitCode = jms.Opt(exitCode)
			}
			res.Note = jms.Opt(fmt.Sprintf(
				"the job already finished (rc=%s); its exit marker was left untouched", exitCode))
		case "no_marker":
			res.Note = jms.Opt("this job predates pid markers and cannot be cancelled")
		case "still_running":
			res.Note = jms.Opt("SIGTERM sent but still alive after 5s — state unknown, do not assume it stopped")
		default:
			res.Note = jms.Opt("the machine answered an unrecognized state; re-check with jmsx_bg_check")
		}
		return render.Text(res)
	})

}
