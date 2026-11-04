package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/audit"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/render"
)

// The audit trail tool: what the GATE decided, on this machine.
//
// Why it is a tool of the execution plane. The trail is written by both
// servers — the gate is shared, so the gate's voice is shared — and the
// execution plane is where the case it answers for comes from: a DANGEROUS
// command refused for want of a confirmation never reaches the bastion, so
// the bastion has no session and no log line for it. "Who tried to run rm -rf
// on that box and was stopped" has no answer on either side unless this
// machine keeps a trail of its own.
//
// It reads THIS machine's trail, and the distinction matters: the
// management plane's jms_command_audit reads what ran on the assets (the
// bastion's log), this reads what was ATTEMPTED here (the gate's own
// record). The two do not overlap — a refused call is in one and not the
// other.
//
// It reads the directory the server is configured to write to, and no other:
// the trail's location is the operator's decision, and a tool that could be
// pointed anywhere would not be answering "the trail" any more.
func RegisterAuditTools(r *reg.Registry, settings audit.Settings) {
	dir := settings.TrailDir()
	type auditArgs struct {
		Service  string `json:"service,omitempty" jsonschema:"only the lines written by this server, e.g. jumpserver-exec or jumpserver-mgmt"`
		Tool     string `json:"tool,omitempty" jsonschema:"only the lines of this tool, e.g. jmsx_exec"`
		Client   string `json:"client,omitempty" jsonschema:"only the lines of this MCP client, e.g. the agent host's name"`
		Allowed  *bool  `json:"allowed,omitempty" jsonschema:"true keeps the calls that were NOT refused (a failure counts as not refused — look at failed for those), false keeps the ones that were refused, rehearsed or stopped at the gate"`
		Refused  *bool  `json:"refused,omitempty" jsonschema:"true keeps the calls a TOOL refused, false keeps the rest. A gate-level refusal (the call never reached the tool) and a rehearsal are not refusals"`
		Failed   *bool  `json:"failed,omitempty" jsonschema:"true keeps the calls whose WORK failed (the tool answered with an error), false keeps the rest. A call the gate stopped is NOT a failure: the gate working is not something broken"`
		Delivery string `json:"delivery,omitempty" jsonschema:"only the lines with this delivery state: not_sent (it never left this process) or maybe_sent (it may ALREADY be running on the machine)"`
		Since    string `json:"since,omitempty" jsonschema:"only the lines at or after this moment, e.g. 2026-10-28 or 2026-10-28T09:00:00"`
		Limit    int    `json:"limit,omitempty" jsonschema:"how many lines to return at most (default 50, at most 500)"`
	}
	reg.Add(r, &mcp.Tool{
		Name:         "jmsx_audit_query",
		Description:  "Read this machine's gate trail: one line per tool call, with the arguments, whether the call did anything and why not. Use it when a call was REFUSED and there is nothing else to look up — a dangerous command refused for want of a confirmation never reaches the bastion, so the bastion's own audit has no record of it and this is the only one. It also answers 'which tools ran here recently', 'what failed here' (failed=true picks those out) and, most importantly, 'what may still be RUNNING on that machine?' — delivery=maybe_sent picks out the calls whose command may still be going, and delivery=not_sent the ones that never left this process (safe to run again). This is the local trail of attempted calls; the management plane's jms_command_audit is the bastion's log of what actually ran on the assets.",
		OutputSchema: render.Schema[jmsexec.AuditQueryResult](),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args auditArgs) (*mcp.CallToolResult, any, error) {
		delivery := strings.TrimSpace(args.Delivery)
		if delivery != "" {
			// The delivery states are a CLOSED set, so a value that is
			// not one of them is a mistake — and a filter that silently
			// matches nothing would answer "nothing may still be running
			// out there", which is the most dangerous silence this tool
			// could produce.
			switch delivery {
			case jms.Delivery_not_sent.String(), jms.Delivery_maybe_sent.String():
			default:
				return render.Failf("unknown delivery state %q; it is %q or %q",
					delivery,
					jms.Delivery_not_sent.String(),
					jms.Delivery_maybe_sent.String())
			}
		}
		opts := audit.ReadOptions{
			Dir:      dir,
			Service:  strings.TrimSpace(args.Service),
			Tool:     strings.TrimSpace(args.Tool),
			Client:   strings.TrimSpace(args.Client),
			Allowed:  args.Allowed,
			Refused:  args.Refused,
			Failed:   args.Failed,
			Delivery: delivery,
			Limit:    args.Limit,
		}
		if s := strings.TrimSpace(args.Since); s != "" {
			since, err := parseSince(s)
			if err != nil {
				return render.Failf("%v", err)
			}
			opts.Since = since
		}
		res, err := audit.Read(opts)
		if err != nil {
			return render.Failf("%v", err)
		}
		entries := res.Entries
		rows := make([]*jmsexec.AuditEntry, 0, len(entries))
		for _, e := range entries {
			row := &jmsexec.AuditEntry{
				Ts:         e.Ts,
				Service:    e.Service,
				Tool:       e.Tool,
				Allowed:    e.Allowed,
				DurationMs: jms.OptInt64(e.DurationMs),
				OutBytes:   jms.OptInt64(e.OutBytes),
			}
			if e.Client != "" {
				row.Client = jms.Opt(e.Client)
			}
			if e.Args != "" {
				row.Args = jms.Opt(e.Args)
			}
			if e.Refused != "" {
				row.Refused = jms.Opt(e.Refused)
			}
			if e.DryRun {
				row.DryRun = jms.OptBool(true)
				if e.WouldExecute != nil {
					row.WouldExecute = jms.OptBool(*e.WouldExecute)
				}
			}
			if e.Error != "" {
				row.Error = jms.Opt(e.Error)
			}
			if e.Failed {
				row.Failed = jms.OptBool(true)
			}
			if e.Delivery != "" {
				row.Delivery = jms.Opt(e.Delivery)
			}
			for _, st := range e.Stages {
				stage := &jmsexec.AuditStage{Stage: st.Stage, Allow: st.Allow}
				if st.Reason != "" {
					stage.Reason = jms.Opt(st.Reason)
				}
				row.Stages = append(row.Stages, stage)
			}
			rows = append(rows, row)
		}
		return render.Text(&jmsexec.AuditQueryResult{
			TrailDir:  dir,
			Entries:   rows,
			Count:     jms.OptInt32(int32(len(rows))),
			Total:     jms.OptInt32(int32(res.Total)),
			Truncated: jms.OptBool(res.Total > len(rows)),
			// ⚠️ The count of the lines the reader could not take is in
			// the CONTRACT, not only in a sentence: an agent that has to
			// parse Chinese prose to learn "this is not everything" is a
			// bad way to say it.
			Skipped: jms.OptInt32(int32(res.Skipped)),
			Note: jms.Opt(fmt.Sprintf(
				"%s本机门禁留痕，最新的在前（返回 %d 行，匹配且读到 %d 行，本次最多 %d 行%s%s）；"+
					"这是「在这里尝试过什么」，不是堡垒机上的「在资产上跑过什么」。",
				trailOffNote(dir),
				len(rows), res.Total, effectiveLimit(args.Limit), skippedNote(res.Skipped),
				retentionNote(settings.RetentionDays))),
		})
	})
}

// trailOffNote says the trail is OFF, when it is.
//
// ⚠️ Without it the answer reads like an empty trail: count=0, total=0,
// truncated=false, skipped=0 — every number says "that is everything",
// and the truth is that this machine kept nothing at all. A refused call
// that leaves no trace anywhere is exactly what this tool exists to answer
// for, so an answer that quietly says "nothing happened" is the worst one.
func trailOffNote(dir string) string {
	if dir != "" {
		return ""
	}
	return "⚠️ 本机门禁留痕是**关闭**的（没有配置留痕目录）⇒ 下面没有任何记录，" +
		"本机也没有留下任何东西；"
}

// retentionNote says how far back the trail goes.
//
// ⚠️ Without it, "0 lines matched" is a FALSE silence: the files that held
// the answer may have been pruned by the retention policy, and an agent
// asking "what happened yesterday" would be told "nothing happened
// yesterday" — which is not the same fact as "the machine kept nothing".
func retentionNote(days int) string {
	if days <= 0 {
		return ""
	}
	// ⚠️ The window the pruner actually uses is clamped; the note must
	// not promise more than the machine keeps.
	if days > audit.MaxRetentionDays {
		days = audit.MaxRetentionDays
	}
	// ⚠️ It says what is actually true: the retention is a FILE-level
	// window (a file per process, older than the retention AND whose
	// process is gone ⇒ removed), not a row-level one. A file that is
	// still being written to can hold rows far older than the window —
	// promising "only the last N days" would be a false sentence.
	return fmt.Sprintf("；本机留痕按文件保留 %d 天（每个进程一个文件；旧的文件会被清理，"+
		"但「旧」的判断在部分平台上不精确 ⇒ 更早的行可能还在）", days)
}

// effectiveLimit is the limit that was in force for this call.
//
// ⚠️ NOT the ceiling: the ceiling (audit.MaxReadLimit) is what the trail
// allows, and the limit in force is what this call asked for (or the
// default). Saying "at most 500" when the call asked for 50 is a true
// sentence about the wrong thing.
func effectiveLimit(asked int) int {
	switch {
	case asked <= 0:
		return audit.DefaultReadLimit
	case asked > audit.MaxReadLimit:
		return audit.MaxReadLimit
	default:
		return asked
	}
}

// skippedNote says how many lines the reader could not take.
//
// ⚠️ It has to be said: a reader that skipped lines has NOT seen the whole
// trail, and an answer that says "that is everything" while quietly
// dropping lines is the same true-looking silence the trail exists to end.
//
// ⚠️ It says "could not read" rather than "too long": a line that does not
// parse is just as unreadable as a line that is too long, and the two are
// counted together — so a wording that named only one of them would be
// wrong half the time.
//
// ⚠️ And it says "may be one of yours": the count is of the unreadable
// lines that could be about what was asked for, so it is not a promise
// that something important is missing — it is a warning that something
// might be.
func skippedNote(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("，另有 %d 行读不了（太长或写坏了，其中可能有你关心的）", n)
}

// parseSince reads the moment a query starts from.
//
// Three spellings are accepted, because all three are things a person would
// type: a date, a date and a time, and a full timestamp.
func parseSince(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a moment this tool understands; use 2026-10-28, 2026-10-28 09:00:00 or a full RFC3339 timestamp", s)
}
