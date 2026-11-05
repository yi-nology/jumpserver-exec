package handler

// The introspection tools: what the connection pool holds right now,
// and what one bastion session costs.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/render"
)

// The introspection tools: what the connection pool holds right now,
// and what one bastion session costs.

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

func registerPoolStatusTool(r *reg.Registry, cfg config.Config) {
	type poolArgs struct{}

	reg.Add(r, &mcp.Tool{
		Name: "jmsx_pool_status",
		Description: "Report what the connection pool holds right now: how many connections are open, " +
			"which machines they are on, and how long each has been idle. Use it when a repeat call on " +
			"the same machine is unexpectedly fast and you want to know why, or to see what is still open " +
			"on the bastion. Read-only.",
		OutputSchema: render.Schema[jmsexec.PoolStatus]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args poolArgs) (*mcp.CallToolResult, any, error) {
		// The TTL the pool actually runs with — JMS_POOL_TTL, not the
		// compiled-in default — is the one the answer has to quote, or an
		// operator who raised it reads a stale number here and in doctor.
		ttl := koko.PoolTTL
		backend := cfg.Koko.Backend
		if backend == "" {
			backend = koko.BackendAuto
		}
		if cfg.Koko.Pool != nil {
			ttl = cfg.Koko.Pool.TTL()
		}
		status := &jmsexec.PoolStatus{
			IdleTTLSeconds: ttl.Seconds(),
			Backend:        jms.Opt(backend),
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

}

func registerSessionCostTool(r *reg.Registry, cfg config.Config) {

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
