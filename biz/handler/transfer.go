package handler

// tools_transfer.go exposes the session and file-transfer primitives as MCP
// tools.
//
// Each tool opens one koko session, does its work, and closes it -- a tool
// call must not leave a shell hanging on the bastion. Within a call the
// connection is reused, which is where the speedup lives.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/oekit/pkg/slicex"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oekit/concurrency/async"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oekit/pkg/encoding"
	"github.com/oekit/pkg/shellx"
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/render"

	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/risk"
)

// runAcrossAssets runs work on every asset, in parallel, and returns the
// results in the order the assets were given.
//
// The work for one asset is a whole session, and the assets are independent
// of each other, so the sessions overlap. That is where the speed comes
// from: establishing a koko session costs about ten seconds, and paying
// those one after another turns a ten-host sweep into two minutes of
// waiting.
//
// Results keep the input order so the caller can line them up with the
// asset list without sorting.
func runAcrossAssets(cfg config.Config, assets []string, concurrency int, work func(asset string) *jmsexec.MatrixRow) []*jmsexec.MatrixRow {
	results := make([]*jmsexec.MatrixRow, len(assets))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		// async, not a bare goroutine: a panic in one host's work would
		// otherwise take the whole server down with it, and the other
		// hosts' results with it.
		async.GoSafe(func() {
			defer wg.Done()
			for idx := range jobs {
				results[idx] = work(assets[idx])
			}
		})
	}
	for idx := range assets {
		jobs <- idx
	}
	close(jobs)
	wg.Wait()
	return results
}

func RegisterTransferTools(r *reg.Registry, cfg config.Config) {
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
			res.Risk = jms.Opt(riskClass)
			res.DryRun = jms.OptBool(true)
			res.WouldExecute = jms.OptBool(would)
			res.Note = jms.Opt(dryRunNote)
			if blocked != "" {
				res.Blocked = jms.Opt(blocked)
			}
			return render.Text(res)
		}
		if !would {
			res.Risk = jms.Opt(riskClass)
			res.Blocked = jms.Opt(blocked)
			return render.Text(res)
		}
		timeout := time.Duration(args.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
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
			res.Error = jms.Opt(runErr.Error())
			res.Partial = jms.OptBool(true)
			// How far the commands got. The trail asks "what may still be
			// running on that machine?", and this answer is where the
			// answer lives — the call itself succeeded, so nothing else
			// would say it.
			if d := reg.DeliveryOf(runErr); d != jms.Delivery(0) {
				res.Delivery = jms.Opt(d.String())
			}
		}
		return render.Text(res)
	})

	type putArgs struct {
		Asset      string `json:"asset" jsonschema:"asset name or address"`
		LocalPath  string `json:"local_path,omitempty" jsonschema:"file on this machine to upload; use instead of content when the file already exists locally"`
		Content    string `json:"content,omitempty" jsonschema:"the file content to upload, given inline. Use this when the content is already in hand (a generated script, a config fragment) so no temp file is needed."`
		ContentB64 string `json:"content_base64,omitempty" jsonschema:"same as content, but base64-encoded; use for binary payloads"`
		RemotePath string `json:"remote_path" jsonschema:"destination path on the asset"`
		Mode       string `json:"mode,omitempty" jsonschema:"chmod mode for the remote file, e.g. 700 for a script"`
		Timeout    int    `json:"timeout,omitempty" jsonschema:"seconds per internal step (default 60)"`
		Confirm    bool   `json:"confirm" jsonschema:"must be true; this writes a file on the machine, overwriting what is there"`
	}
	reg.Add(r, &mcp.Tool{
		Name: "jmsx_put_file",
		Description: "Upload a file to an asset through the bastion, over the same recorded session used for commands. " +
			"Suitable for deploy scripts, config fragments and certificates. The file is written to a temporary name and " +
			"renamed into place, so a transfer that fails halfway leaves the file that was there before untouched, and a " +
			"symlinked destination keeps its link. The size is verified after the write, so a truncated " +
			"transfer fails loudly instead of leaving a corrupt file behind. Content is base64, so nothing is interpreted by the shell. " +
			"Supply the payload either as content (inline text), content_base64 (inline binary), or local_path (a file already on this machine) -- exactly one of them.",
		OutputSchema: render.Schema[jmsexec.PutFileResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args putArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.RemotePath) == "" {
			return render.Failf("remote_path is required")
		}
		sources := []string{}
		if args.LocalPath != "" {
			sources = append(sources, "local_path")
		}
		if args.Content != "" {
			sources = append(sources, "content")
		}
		if args.ContentB64 != "" {
			sources = append(sources, "content_base64")
		}
		switch len(sources) {
		case 0:
			return render.Failf("nothing to upload: pass local_path, content or content_base64")
		case 1:
		default:
			return render.Failf("pass exactly one of local_path, content or content_base64 (got %s)",
				strings.Join(sources, ", "))
		}
		timeout := time.Duration(args.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 60 * time.Second
		}

		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, timeout+120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()

		var tr *koko.TransferResult
		switch {
		case args.LocalPath != "":
			st, err := os.Stat(args.LocalPath)
			if err != nil {
				return render.Failf("%v", err)
			}
			if st.IsDir() {
				return render.Failf("%s is a directory; only single files are supported", args.LocalPath)
			}
			tr, err = s.PutFile(args.LocalPath, args.RemotePath, timeout)
			if err != nil {
				// The typed error, not its text: a half-written file is
				// exactly the case where the caller has to know whether
				// the bytes reached the machine (see jmsx_exec).
				return fail(err)
			}
		case args.Content != "":
			tr, err = s.PutBytes([]byte(args.Content), args.RemotePath, timeout)
			if err != nil {
				return fail(err)
			}
			tr.LocalPath = "(inline content)"
		default:
			raw, err := encoding.Base64DecodeBytes(args.ContentB64)
			if err != nil {
				return render.Failf("content_base64 is not valid base64: %v", err)
			}
			tr, err = s.PutBytes(raw, args.RemotePath, timeout)
			if err != nil {
				return fail(err)
			}
			tr.LocalPath = "(inline base64)"
		}
		if args.Mode != "" {
			if _, _, _, err := s.Run(
				fmt.Sprintf("chmod %s %s", args.Mode, args.RemotePath), timeout); err != nil {
				return render.Failf("uploaded, but chmod failed: %v", err)
			}
			tr.Mode = args.Mode
		}
		out := &jmsexec.PutFileResult{
			Asset:              jms.Opt(tr.Asset),
			Local:              jms.Opt(tr.LocalPath),
			Remote:             jms.Opt(tr.RemotePath),
			Bytes:              jms.OptInt64(int64(tr.Bytes)),
			Chunks:             jms.OptInt32(int32(tr.Chunks)),
			Seconds:            jms.OptFloat64(tr.Seconds),
			RemoteSha256Prefix: jms.Opt(tr.RemoteSHA256),
			LocalSha256Prefix:  jms.Opt(tr.LocalSHA256),
			ChecksumMatch:      jms.OptBool(tr.ChecksumMatch),
			Stats:              s.Stats(),
		}
		if tr.Mode != "" {
			out.Mode = jms.Opt(tr.Mode)
		}
		if !tr.ChecksumMatch {
			// The size matched, so the size check did not catch this. Say so
			// plainly: a file whose content does not match is worse than a
			// transfer that failed loudly.
			out.Warning = jms.Opt("the checksum on the host does not match the bytes that were sent, " +
				"so the file is not intact even though its size matches. Re-upload, and do not " +
				"rely on this file")
		}
		return render.Text(out)
	})

	type getArgs struct {
		Asset      string `json:"asset" jsonschema:"asset name or address"`
		RemotePath string `json:"remote_path" jsonschema:"file on the asset to read"`
		LocalPath  string `json:"local_path,omitempty" jsonschema:"write the content to this local path"`
		Offset     int64  `json:"offset,omitempty" jsonschema:"byte offset in the file to start reading from (default 0)"`
		Length     int64  `json:"length,omitempty" jsonschema:"how many bytes to read from that offset; 0 (the default) means to the end of the file"`
		Timeout    int    `json:"timeout,omitempty" jsonschema:"seconds (default 60)"`
	}
	reg.Add(r, &mcp.Tool{
		Name: "jmsx_get_file",
		Description: "Read a file FROM an asset through the bastion and return its content (base64-encoded over the wire, " +
			"decoded for you). Pass offset and length to read only part of it: the window is applied on the remote side, " +
			"so peeking at the head of a huge log costs only the bytes you asked for. " +
			"A whole file is held in memory on this machine, so for a big one ask for a window rather than the lot. " +
			"On the koko terminal path the answer is additionally bounded by the pane (a few dozen rows): larger files " +
			"come back with truncated=true and the full size in available_bytes, so narrow the range server-side with " +
			"head/sed/tail rather than assuming you saw everything.",
		OutputSchema: render.Schema[jmsexec.GetFileResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args getArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.RemotePath) == "" {
			return render.Failf("remote_path is required")
		}
		timeout := time.Duration(args.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, timeout+120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()

		res, err := s.GetFileRange(args.RemotePath, args.LocalPath, args.Offset, args.Length, timeout)
		if err != nil {
			return fail(err)
		}
		out := &jmsexec.GetFileResult{
			Asset:              jms.Opt(res.Asset),
			Remote:             jms.Opt(res.RemotePath),
			Local:              jms.Opt(res.LocalPath),
			BytesReturned:      jms.OptInt64(int64(res.Bytes)),
			AvailableBytes:     jms.OptInt64(res.Available),
			Offset:             jms.OptInt64(res.Offset),
			Length:             jms.OptInt64(res.Length),
			Truncated:          jms.OptBool(res.Truncated),
			Seconds:            jms.OptFloat64(res.Seconds),
			LocalSha256Prefix:  jms.Opt(res.LocalSHA256),
			RemoteSha256Prefix: jms.Opt(res.RemoteSHA256),
			ChecksumMatch:      jms.OptBool(res.ChecksumMatch),
		}
		if res.LocalPath == "" {
			out.Content = jms.Opt(res.Content)
		}
		// The two hashes are only comparable for a whole-file read: a window
		// covers part of the file, so they are expected to differ there.
		fullRead := args.Offset == 0 && (args.Length == 0 || args.Length == res.Available)
		// There is one warning field, and two different things can be wrong
		// with the same download — so both are said, rather than the second
		// quietly replacing the first.
		var warnings []string
		if fullRead && !res.ChecksumMatch {
			warnings = append(warnings, "the checksum of the bytes that arrived does not match the file on the host, "+
				"so the copy is not intact. Re-download, and do not use this copy")
		}
		if res.Truncated {
			warnings = append(warnings, fmt.Sprintf(
				"file is %d bytes but only %d could be read back; narrow it server-side "+
					"(head -c, tail -c, sed -n) and fetch the ranges you need",
				res.Available, res.Bytes))
		}
		if len(warnings) > 0 {
			out.Warning = jms.Opt(strings.Join(warnings, "; "))
		}
		return render.Text(out)
	})

	type relayArgs struct {
		Source          string `json:"source" jsonschema:"source asset name or address, e.g. node-071 or 192.0.2.71"`
		SourcePath      string `json:"source_path" jsonschema:"path of the file on the source machine, e.g. /opt/app/config.yml"`
		Destination     string `json:"destination" jsonschema:"destination asset name or address"`
		DestinationPath string `json:"destination_path" jsonschema:"path to write the file to on the destination machine"`
		Timeout         int    `json:"timeout,omitempty" jsonschema:"seconds to wait (default 120)"`
		Confirm         bool   `json:"confirm" jsonschema:"must be true: this writes a file on the destination machine"`
	}
	reg.Add(r, &mcp.Tool{
		Name: "jmsx_relay",
		Description: "Copy a file from one managed machine DIRECTLY to another through the bastion, " +
			"without the bytes ever landing on this machine. Use it when a file has to get from one host " +
			"to another and pulling it here and pushing it there would be two transfers and a copy in " +
			"between. Both sides are recorded bastion sessions; the answer reports the checksum computed " +
			"on each machine, so a mismatch is visible rather than silent.",
		OutputSchema: render.Schema[jmsexec.RelayResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args relayArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Source) == "" || strings.TrimSpace(args.Destination) == "" {
			return render.Failf("both source and destination are required")
		}
		if strings.TrimSpace(args.SourcePath) == "" || strings.TrimSpace(args.DestinationPath) == "" {
			return render.Failf("both source_path and destination_path are required")
		}
		timeout := time.Duration(args.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 120 * time.Second
		}
		res, err := koko.Relay(ctx, cfg.Koko, args.Source, args.SourcePath,
			args.Destination, args.DestinationPath, timeout)
		if err != nil {
			return fail(err)
		}
		out := &jmsexec.RelayResult{
			SourceAsset:             res.SourceAsset,
			SourcePath:              res.SourcePath,
			DestinationAsset:        res.DestAsset,
			DestinationPath:         res.DestPath,
			Bytes:                   int32(res.Bytes),
			Chunks:                  jms.OptInt32(int32(res.Chunks)),
			Seconds:                 jms.OptFloat64(res.Seconds),
			SourceSha256Prefix:      jms.Opt(res.SourceSHA256),
			DestinationSha256Prefix: jms.Opt(res.DestSHA256),
			ChecksumMatch:           jms.OptBool(res.ChecksumMatch),
		}
		note := "the bytes went from the source machine's channel straight onto the destination's; " +
			"they never landed on this machine"
		if !res.ChecksumMatch {
			note = "the two checksums differ, so the copy is NOT intact; do not use it"
		}
		out.Note = jms.Opt(note)
		return render.Text(out)
	})

	type poolArgs struct{}
	reg.Add(r, &mcp.Tool{
		Name: "jmsx_pool_status",
		Description: "Report what the connection pool holds right now: how many connections are open, " +
			"which machines they are on, and how long each has been idle. Use it when a repeat call on " +
			"the same machine is unexpectedly fast and you want to know why, or to see what is still open " +
			"on the bastion. Read-only.",
		OutputSchema: render.Schema[jmsexec.PoolStatus]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args poolArgs) (*mcp.CallToolResult, any, error) {
		status := &jmsexec.PoolStatus{
			IdleTTLSeconds: koko.PoolTTL.Seconds(),
			Backend:        jms.Opt(cfg.Koko.Backend),
		}
		if status.Backend == nil || *status.Backend == "" {
			status.Backend = jms.Opt(koko.BackendAuto)
		}
		if cfg.Koko.Pool == nil {
			status.Connections = 0
			status.Note = jms.Opt("no connection pool is configured; every session opens its own connection")
			return render.Text(status)
		}
		entries := cfg.Koko.Pool.Entries()
		status.Connections = int32(len(entries))
		rows := make([]*jmsexec.PoolEntry, 0, len(entries))
		for _, e := range entries {
			rows = append(rows, &jmsexec.PoolEntry{
				Asset:       jms.Opt(e.Asset),
				Account:     jms.Opt(e.Account),
				Identity:    jms.Opt(e.Identity),
				IdleSeconds: jms.OptFloat64(e.IdleSeconds),
			})
		}
		status.Entries = rows
		status.Note = jms.Opt("a pooled connection is not a new session: it reuses the one that is " +
			"already open, so no token is minted and no handshake is needed. An idle connection is closed " +
			"after the TTL above")
		return render.Text(status)
	})

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
			res.Risk = jms.Opt(riskClass)
			res.DryRun = jms.OptBool(true)
			res.WouldExecute = jms.OptBool(would)
			res.Note = jms.Opt(dryRunNote)
			if blocked != "" {
				res.Blocked = jms.Opt(blocked)
			}
			return render.Text(res)
		}
		if !would {
			res.Risk = jms.Opt(riskClass)
			res.Blocked = jms.Opt(blocked)
			return render.Text(res)
		}
		timeout := time.Duration(args.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, timeout+120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()

		commands := make([]string, 0, len(args.Checks))
		for _, c := range args.Checks {
			commands = append(commands, c.Command)
		}
		outputs, runErr := s.Exec(commands, timeout)
		facts := map[string]*jmsexec.Fact{}
		for i, c := range args.Checks {
			entry := &jmsexec.Fact{Command: jms.Opt(c.Command)}
			if i < len(outputs) {
				entry.Output = jms.Opt(outputs[i])
			}
			facts[c.Name] = entry
		}
		res.Facts = facts
		res.Stats = s.Stats()
		if runErr != nil {
			res.Error = jms.Opt(runErr.Error())
			res.Partial = jms.OptBool(true)
			// How far the commands got. The trail asks "what may still be
			// running on that machine?", and this answer is where the
			// answer lives — the call itself succeeded, so nothing else
			// would say it.
			if d := reg.DeliveryOf(runErr); d != jms.Delivery(0) {
				res.Delivery = jms.Opt(d.String())
			}
		}
		return render.Text(res)
	})

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
			res.Risk = jms.Opt(riskClass)
			res.Blocked = jms.Opt(blocked)
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
			res.Risk = jms.Opt(riskClass)
			res.DryRun = jms.OptBool(true)
			res.WouldExecute = jms.OptBool(would)
			res.Note = jms.Opt(dryRunNote)
			if blocked != "" {
				res.Blocked = jms.Opt(blocked)
			}
			res.Plan = planSweep(ctx, cfg, args.Assets, len(args.Checks), concurrency, effectiveTimeout(args.Timeout))
			return render.Text(res)
		}
		if !would {
			res.Risk = jms.Opt(riskClass)
			res.Blocked = jms.Opt(blocked)
			res.Plan = planSweep(ctx, cfg, args.Assets, len(args.Checks), concurrency, effectiveTimeout(args.Timeout))
			return render.Text(res)
		}

		started := time.Now()
		// Whether any host's command may still be running there. The
		// call-level state can only be "maybe sent" — the other hosts ran
		// their commands, so "not sent" would be a lie.
		var maybeRunning atomic.Bool
		results := runAcrossAssets(cfg, args.Assets, concurrency, func(asset string) *jmsexec.MatrixRow {
			entry := &jmsexec.MatrixRow{Asset: jms.Opt(asset)}
			hostStarted := time.Now()
			s, err := koko.OpenSession(ctx, cfg.Koko, asset, timeout+180*time.Second)
			if err != nil {
				entry.Ok = jms.OptBool(false)
				entry.Error = jms.Opt(err.Error())
				// The session never opened, so the commands never left
				// this process. Saying so is what tells the operator
				// "this host never even started" apart from "this host
				// may still be running it".
				if d := reg.DeliveryOf(err); d != jms.Delivery(0) {
					entry.Delivery = jms.Opt(d.String())
				}
				entry.Seconds = jms.OptFloat64(time.Since(hostStarted).Seconds())
				return entry
			}
			defer s.Close()
			// One session, all the checks: the facts of one host cannot mix
			// with another's, and the session is opened once per host.
			commands := slicex.Map(args.Checks, func(c collectCheck) string { return c.Command })
			outputs, runErr := s.Exec(commands, timeout)
			facts := map[string]*jmsexec.Fact{}
			for i, c := range args.Checks {
				f := &jmsexec.Fact{Command: jms.Opt(c.Command)}
				if i < len(outputs) {
					f.Output = jms.Opt(outputs[i])
				}
				facts[c.Name] = f
			}
			entry.Facts = facts
			entry.Ok = jms.OptBool(runErr == nil)
			if runErr != nil {
				entry.Error = jms.Opt(runErr.Error())
				if d := reg.DeliveryOf(runErr); d != jms.Delivery(0) {
					entry.Delivery = jms.Opt(d.String())
				}
				if reg.DeliveryOf(runErr) == jms.Delivery_maybe_sent {
					maybeRunning.Store(true)
				}
			}
			entry.Seconds = jms.OptFloat64(time.Since(hostStarted).Seconds())
			return entry
		})

		okCount, failCount := 0, 0
		matrix := map[string]*jmsexec.MatrixRow{}
		firstFailure := ""
		allNotSent := true
		for _, r := range results {
			if r == nil {
				continue
			}
			// Every host goes into the matrix — the succeeded ones too.
			matrix[r.GetAsset()] = r
			if r.GetOk() {
				okCount++
				continue
			}
			failCount++
			if firstFailure == "" {
				firstFailure = r.GetError()
			}
			if r.GetDelivery() != jms.Delivery_not_sent.String() {
				allNotSent = false
			}
		}
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
			res.Risk = jms.Opt(riskClass)
			res.DryRun = jms.OptBool(true)
			res.WouldExecute = jms.OptBool(would)
			res.Note = jms.Opt(dryRunNote)
			if blocked != "" {
				res.Blocked = jms.Opt(blocked)
			}
			res.Plan = planSweep(ctx, cfg, args.Assets, 1, concurrency, effectiveTimeout(args.Timeout))
			return render.Text(res)
		}
		if !would {
			res.Risk = jms.Opt(riskClass)
			res.Blocked = jms.Opt(blocked)
			res.Plan = planSweep(ctx, cfg, args.Assets, 1, concurrency, effectiveTimeout(args.Timeout))
			return render.Text(res)
		}

		// Every host gets its own session, so the hosts are independent and can
		// run at the same time. Opening a session used to cost about ten
		// seconds per host and was paid one host after another: a ten-host
		// sweep took two minutes for work that takes seconds once the
		// openings overlap.
		started := time.Now()
		results := make([]*jmsexec.HostResult, len(args.Assets))
		jobs := make(chan int)
		var (
			wg       sync.WaitGroup
			stopped  atomic.Bool
			firstErr atomic.Bool
			// Whether any host's command may still be running there. The
			// call-level state can only be "maybe sent" — the other hosts
			// ran their commands, so "not sent" would be a lie.
			maybeRunning atomic.Bool
		)

		worker := func() {
			defer wg.Done()
			for idx := range jobs {
				asset := args.Assets[idx]
				if strings.TrimSpace(asset) == "" {
					continue
				}
				entry := &jmsexec.HostResult{Asset: jms.Opt(asset)}
				hostStarted := time.Now()
				s, err := koko.OpenSession(ctx, cfg.Koko, asset, timeout+120*time.Second)
				if err != nil {
					entry.Ok = jms.OptBool(false)
					entry.Error = jms.Opt(err.Error())
					// The session never opened, so the command never
					// left this process.
					if d := reg.DeliveryOf(err); d != jms.Delivery(0) {
						entry.Delivery = jms.Opt(d.String())
					}
					entry.Seconds = jms.OptFloat64(time.Since(hostStarted).Seconds())
					results[idx] = entry
					firstErr.Store(true)
					if args.StopOnError {
						stopped.Store(true)
					}
					continue
				}
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
					entry.Error = jms.Opt(runErr.Error())
					if d := reg.DeliveryOf(runErr); d != jms.Delivery(0) {
						entry.Delivery = jms.Opt(d.String())
					}
					if reg.DeliveryOf(runErr) == jms.Delivery_maybe_sent {
						maybeRunning.Store(true)
					}
					firstErr.Store(true)
					if args.StopOnError {
						stopped.Store(true)
					}
				} else {
					entry.Ok = jms.OptBool(true)
				}
				s.Close()
				results[idx] = entry
			}
		}

		wg.Add(concurrency)
		for i := 0; i < concurrency; i++ {
			async.GoSafe(worker)
		}
	feed:
		for idx := range args.Assets {
			if stopped.Load() {
				results[idx] = &jmsexec.HostResult{
					Asset:   jms.Opt(args.Assets[idx]),
					Ok:      jms.OptBool(false),
					Skipped: jms.OptBool(true),
					Error:   jms.Opt("not attempted: stop_on_error was set and an earlier host failed"),
					// It was never attempted, so nothing was sent. Saying
					// so keeps the column uniform: every host that did
					// not run says so.
					Delivery: jms.Opt(jms.Delivery_not_sent.String()),
				}
				continue
			}
			select {
			case jobs <- idx:
			case <-time.After(timeout + 180*time.Second):
				break feed
			}
		}
		close(jobs)
		wg.Wait()

		okCount, failCount, skipped := 0, 0, 0
		ordered := []*jmsexec.HostResult{}
		firstFailure := ""
		allNotSent := true
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
				continue
			}
			if r.GetOk() {
				okCount++
				continue
			}
			failCount++
			if firstFailure == "" {
				firstFailure = r.GetError()
			}
			if r.GetDelivery() != jms.Delivery_not_sent.String() {
				allNotSent = false
			}
		}
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

	type infoArgs struct {
		Asset string `json:"asset" jsonschema:"asset name or address"`
	}
	reg.Add(r, &mcp.Tool{
		Name:         "jmsx_session_cost",
		Description:  "Report what one bastion session costs: how long opening it takes and how much a session-based tool call saves versus repeated jmsx_exec calls. Use it to price a sweep before running it: opening the session is what a fleet sweep really pays, not the commands. The answer names the backend the session was opened with, because the two cost very differently.",
		OutputSchema: render.Schema[jmsexec.SessionCostResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args infoArgs) (*mcp.CallToolResult, any, error) {
		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, 120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()
		out, _, _, _ := s.Run("echo session-ready", 30*time.Second)
		b, _ := json.MarshalIndent(out, "", "  ")
		stats := s.Stats()
		return render.Text(&jmsexec.SessionCostResult{
			Asset:       jms.Opt(args.Asset),
			Probe:       jms.Opt(strings.TrimSpace(string(b))),
			Stats:       stats,
			WalkSeconds: stats.WalkSeconds,
			Note:        jms.Opt(sessionCostNote(stats)),
		})
	})
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
type collectCheck struct {
	Name    string `json:"name" jsonschema:"label for this fact, e.g. 'disk'"`
	Command string `json:"command" jsonschema:"the command that produces it"`
}

// sessionCostNote explains what the measured numbers are for.
//
// The wording follows the backend, because the two paths do not cost the
// same thing: a session opened with a connection token pays for a token and
// a handshake, and one opened from the pool pays for neither.
func sessionCostNote(stats *jmsexec.SessionStats) string {
	if stats.GetReused() {
		return "this session came from the connection pool, so no token was minted and no handshake " +
			"was needed; everything after the opening in the same session is nearly free. " +
			"This is the same measured cost the dry-run plan of a fleet sweep prices itself with"
	}
	return "opening the session dominates; everything after it in the same session is nearly free. " +
		"This is the same measured cost the dry-run plan of a fleet sweep prices itself with"
}
