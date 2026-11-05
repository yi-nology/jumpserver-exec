package handler

// The background tools: start a long-running command detached from the
// session (it survives the bastion closing the connection), and check
// whether it has finished.

import (
	"context"
	"fmt"
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
			path = fmt.Sprintf("/tmp/jmsx-bg-%d.log", time.Now().UnixNano())
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
		wrapper := `sh -c 'sh -c "$1"; echo $? > "$2"' _ ` + shellx.Quote(args.Command) + " " + shellx.Quote(marker)
		start := fmt.Sprintf("rm -f %s %s; setsid nohup %s > %s 2>&1 < /dev/null & echo started",
			path, marker, wrapper, path)
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

func registerBgCheckTool(r *reg.Registry, cfg config.Config) {

	type bgCheckArgs struct {
		Asset string `json:"asset" jsonschema:"asset name or address"`
		Path  string `json:"path" jsonschema:"the output path returned by jmsx_bg_start"`
	}

	reg.Add(r, &mcp.Tool{
		Name: "jmsx_bg_check",
		Description: "Check whether a background command started with jmsx_bg_start is still running, and whether " +
			"it has finished. Read the output itself with jmsx_get_file.",
		OutputSchema: render.Schema[jmsexec.BgCheckResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args bgCheckArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Path) == "" {
			return render.Failf("path is required")
		}
		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, 120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()

		out, _, _, err := s.Run(fmt.Sprintf(
			"if [ -f %s.rc ]; then echo FINISHED rc=$(cat %s.rc); else echo RUNNING; fi; wc -c < %s 2>/dev/null",
			args.Path, args.Path, args.Path), 60*time.Second)
		if err != nil {
			return fail(err)
		}
		state := "running"
		exitCode := ""
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "FINISHED") {
				state = "finished"
				if i := strings.Index(line, "rc="); i >= 0 {
					exitCode = strings.TrimSpace(line[i+3:])
				}
			}
		}
		res := &jmsexec.BgCheckResult{
			Asset: jms.Opt(args.Asset),
			Path:  jms.Opt(args.Path),
			State: jms.Opt(state),
			Next:  jms.Opt("read the output with jmsx_get_file"),
		}
		if exitCode != "" {
			res.ExitCode = jms.Opt(exitCode)
		}
		return render.Text(res)
	})

}
