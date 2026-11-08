package handler

// The file tools: upload to a machine, download from one, and copy
// machine-to-machine without the bytes landing here — all over the same
// recorded bastion sessions the command tools use.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oekit/pkg/encoding"
	"github.com/oekit/pkg/shellx"
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/render"
)

// The file tools: upload to a machine, download from one, and copy
// machine-to-machine without the bytes landing here — all over the same
// recorded bastion sessions the command tools use.

// modePattern is the chmod whitelist for jmsx_put_file's mode field:
// 3-4 octal digits and nothing else. Anything else is REJECTED, not
// sanitized — a permissive pattern here is a shell line in disguise.
var modePattern = regexp.MustCompile(`^[0-7]{3,4}$`)

func registerPutFileTool(r *reg.Registry, cfg config.Config) {

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
		timeout := commandTimeout(args.Timeout, 60)

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
			// The mode is a PERM BITS field, not a free-form argument to a
			// chmod line. Two gates, in order: a 3-4 octal digit whitelist
			// (schema tags cannot express a pattern here, so the handler
			// owns it), then quoting — so even a value that someday slips
			// past the whitelist stays one argument. The reason this is
			// two gates and not one: the classifier only ever sees the
			// tool's name, so a `; rm -rf` smuggled through this field
			// would run OUTSIDE every gate and trail this repo has.
			if !modePattern.MatchString(args.Mode) {
				return render.Failf("mode must be 3-4 octal digits (e.g. 700, 0644), got %q", args.Mode)
			}
			if _, _, _, err := s.Run(
				fmt.Sprintf("chmod %s %s", shellx.Quote(args.Mode), shellx.Quote(args.RemotePath)), timeout); err != nil {
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

}

func registerPutDirTool(r *reg.Registry, cfg config.Config) {

	type putDirArgs struct {
		Asset      string `json:"asset" jsonschema:"asset name or address"`
		LocalPath  string `json:"local_path" jsonschema:"directory on this machine to upload"`
		RemotePath string `json:"remote_path" jsonschema:"destination directory on the asset; created if missing"`
		Timeout    int    `json:"timeout,omitempty" jsonschema:"seconds per internal step (default 300)"`
		Confirm    bool   `json:"confirm" jsonschema:"must be true; this writes files on the machine, overwriting same-name ones"`
	}

	reg.Add(r, &mcp.Tool{
		Name: "jmsx_put_dir",
		Description: "Upload a whole LOCAL directory to an asset through the bastion, as one tar stream on the recorded session's channel. " +
			"Use it to deploy a config tree, a script bundle or a small application directory in one call instead of one jmsx_put_file per file. " +
			"The semantics are MERGE, not mirror: the archive extracts into the destination, overwriting same-name files and leaving " +
			"everything the host already had alone — to make the remote tree exactly the local one, clear the destination first " +
			"(jmsx_exec) and then upload. Requires the connection-token path and tar on the asset; the koko terminal backend " +
			"answers honestly that it cannot stream a binary payload.",
		OutputSchema: render.Schema[jmsexec.PutDirResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args putDirArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.LocalPath) == "" {
			return render.Failf("local_path is required")
		}
		if strings.TrimSpace(args.RemotePath) == "" {
			return render.Failf("remote_path is required")
		}
		timeout := commandTimeout(args.Timeout, 300)

		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, timeout+120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()

		res, err := s.PutDir(args.LocalPath, args.RemotePath, timeout)
		if err != nil {
			// The typed error, not its text: a half-extracted directory is
			// exactly the case where the caller has to know whether the
			// bytes reached the machine (see jmsx_exec).
			return fail(err)
		}
		out := &jmsexec.PutDirResult{
			Asset:   jms.Opt(res.Asset),
			Local:   jms.Opt(res.LocalDir),
			Remote:  jms.Opt(res.RemoteDir),
			Files:   jms.OptInt32(int32(res.Files)),
			Bytes:   jms.OptInt64(res.Bytes),
			Seconds: jms.OptFloat64(res.Seconds),
			Stats:   s.Stats(),
			Note: jms.Opt("merge semantics: same-name files were overwritten, entries the host already had are untouched. " +
				"To mirror the local tree exactly, clear the destination first"),
		}
		if res.Verified {
			// The arrival proof IS the headline: verified=true means every
			// file the local side hashed landed with those exact bytes.
			out.Verified = jms.OptBool(true)
			out.VerifiedFiles = jms.OptInt32(int32(res.VerifiedFiles))
		}
		if res.Skipped > 0 {
			// The tree that ARRIVED is smaller than the tree that LEFT, and
			// the answer says so instead of letting the caller find out by
			// diffing (a limit is a fact that is reported, not hidden).
			out.Skipped = jms.OptInt32(int32(res.Skipped))
		}
		if res.Note != "" {
			out.Note = jms.Opt(res.Note + " | merge semantics: same-name files were overwritten, " +
				"entries the host already had are untouched. " +
				"verified=false with a reason in the note means arrival could NOT be proven — treat the transfer as unproven")
		}
		return render.Text(out)
	})

}

func registerRelayDirTool(r *reg.Registry, cfg config.Config) {

	type relayDirArgs struct {
		Source          string `json:"source" jsonschema:"source asset name or address"`
		SourcePath      string `json:"source_path" jsonschema:"directory on the source machine, e.g. /opt/app/config"`
		Destination     string `json:"destination" jsonschema:"destination asset name or address"`
		DestinationPath string `json:"destination_path" jsonschema:"directory to extract into on the destination machine; created if missing"`
		Timeout         int    `json:"timeout,omitempty" jsonschema:"seconds per internal step (default 300)"`
		Confirm         bool   `json:"confirm" jsonschema:"must be true; this writes files on the destination machine, overwriting same-name ones"`
	}

	reg.Add(r, &mcp.Tool{
		Name: "jmsx_relay_dir",
		Description: "Copy a whole directory from one managed machine DIRECTLY to another through the bastion: the " +
			"source's tar stream pipes straight into the destination's extractor, and the archive never lands on this " +
			"machine — the directory form of jmsx_relay. Use it to push a config tree or a deployment bundle from one " +
			"host to another without pulling it here and pushing it again. The semantics on the destination are MERGE " +
			"(same-name files overwritten, entries it already had untouched). A straight-through stream carries no " +
			"arrival proof — run a checksum on the destination (jmsx_exec) when the tree must be proven, or move it in " +
			"two hops (jmsx_get_dir then jmsx_put_dir), which verifies with a sha256 manifest. Requires the " +
			"connection-token path and tar on both machines.",
		OutputSchema: render.Schema[jmsexec.RelayDirResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args relayDirArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.SourcePath) == "" {
			return render.Failf("source_path is required")
		}
		if strings.TrimSpace(args.DestinationPath) == "" {
			return render.Failf("destination_path is required")
		}
		timeout := commandTimeout(args.Timeout, 300)

		res, err := koko.RelayDir(ctx, cfg.Koko, args.Source, args.SourcePath, args.Destination, args.DestinationPath, timeout)
		if err != nil {
			// The typed error, not its text: a half-extracted tree on the
			// DESTINATION is exactly the case where the caller has to know
			// how far the copy got (see jmsx_relay).
			return fail(err)
		}
		out := &jmsexec.RelayDirResult{
			SourceAsset: jms.Opt(res.SourceAsset),
			SourcePath:  jms.Opt(res.SourcePath),
			DestAsset:   jms.Opt(res.DestAsset),
			DestPath:    jms.Opt(res.DestPath),
			Bytes:       jms.OptInt64(res.Bytes),
			Seconds:     jms.OptFloat64(res.Seconds),
			Note:        jms.Opt(res.Note),
		}
		return render.Text(out)
	})

}

func registerGetDirTool(r *reg.Registry, cfg config.Config) {

	type getDirArgs struct {
		Asset      string `json:"asset" jsonschema:"asset name or address"`
		RemotePath string `json:"remote_path" jsonschema:"directory on the asset to download"`
		LocalPath  string `json:"local_path" jsonschema:"destination directory on this machine; created if missing"`
		Timeout    int    `json:"timeout,omitempty" jsonschema:"seconds per internal step (default 300)"`
	}

	reg.Add(r, &mcp.Tool{
		Name: "jmsx_get_dir",
		Description: "Download a whole REMOTE directory from an asset through the bastion, as one tar stream on the " +
			"recorded session's channel, extracted here as it arrives — the mirror of jmsx_put_dir. Use it to pull back " +
			"a config tree, a log bundle or an output directory in one call instead of hand-rolled tar|base64 over " +
			"jmsx_exec or one jmsx_get_file per file. The semantics are MERGE: same-name files are overwritten, " +
			"everything the destination already had is untouched. Extraction is defensive: an archive entry that is " +
			"absolute or climbs out of the destination is skipped and counted in `skipped`, never written outside. " +
			"Requires the connection-token path and tar on the asset.",
		OutputSchema: render.Schema[jmsexec.GetDirResult]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args getDirArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.RemotePath) == "" {
			return render.Failf("remote_path is required")
		}
		if strings.TrimSpace(args.LocalPath) == "" {
			return render.Failf("local_path is required")
		}
		timeout := commandTimeout(args.Timeout, 300)

		s, err := koko.OpenSession(ctx, cfg.Koko, args.Asset, timeout+120*time.Second)
		if err != nil {
			return fail(err)
		}
		defer s.Close()

		res, err := s.GetDir(args.RemotePath, args.LocalPath, timeout)
		if err != nil {
			// The typed error, not its text: a half-downloaded directory is
			// exactly the case where the caller has to know whether the
			// bytes left the machine (see jmsx_exec).
			return fail(err)
		}
		out := &jmsexec.GetDirResult{
			Asset:   jms.Opt(res.Asset),
			Remote:  jms.Opt(res.RemoteDir),
			Local:   jms.Opt(res.LocalDir),
			Files:   jms.OptInt32(int32(res.Files)),
			Bytes:   jms.OptInt64(res.Bytes),
			Seconds: jms.OptFloat64(res.Seconds),
			Stats:   s.Stats(),
			// skipped 恒填（零也填）：与 put_dir 对齐——「远端树比到达的树
			// 多出多少」是调用方拥有的一个事实，零是答案，缺席不是。
			Skipped: jms.OptInt32(int32(res.Skipped)),
			Note: jms.Opt(res.Note + " | merge semantics: same-name files were overwritten, " +
				"entries this machine already had are untouched; truncation after the tar " +
				"end-of-archive marker is undetectable by construction — for trees that must " +
				"be proven, compare checksums (jmsx_exec sha256sum)"),
		}
		return render.Text(out)
	})

}

func registerGetFileTool(r *reg.Registry, cfg config.Config) {

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
		timeout := commandTimeout(args.Timeout, 60)
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

}

func registerRelayTool(r *reg.Registry, cfg config.Config) {

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
		timeout := commandTimeout(args.Timeout, 120)
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

}
