// Package router assembles the execution-plane server.
//
// The registration itself lives in jms-kit/pkg/reg, because the gate in
// front of it is the whole point: a tool cannot reach the server without
// passing the capability check.
package router

import (
	"github.com/yi-nology/jumpserver-exec/biz/handler"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/service/policy"
	"github.com/yi-nology/jumpserver-kit/pkg/jmsapi"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
)

// Register puts every tool of the execution plane on the server.
//
// The REST client may be nil: the inventory tool needs it and the command
// tools do not, so a missing token disables one tool instead of the server.
//
// The approval memory is built HERE, once, from the configuration, and
// shared by every conditional-confirmation gate: a confirm=true granted on
// one tool is remembered for the identical call on the others' keys too —
// and the TTL (0 = off) is the one configured value, not a per-tool choice.
func Register(r *reg.Registry, cfg config.Config, c *jmsapi.Client) {
	mem := policy.NewMemory(cfg.ApprovalMemoryTTL)
	handler.RegisterExecTools(r, cfg, mem)
	if c != nil {
		handler.RegisterInventoryTools(r, c)
	}
	handler.RegisterTransferTools(r, cfg, mem)
	// The trail tool needs nothing from the bastion: it reads this
	// machine's own files, so it works even when the REST client is
	// missing.
	handler.RegisterAuditTools(r, cfg.Audit)
}
