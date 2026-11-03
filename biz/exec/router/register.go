// Package router assembles the execution-plane server.
//
// The registration itself lives in jms-kit/pkg/reg, because the gate in
// front of it is the whole point: a tool cannot reach the server without
// passing the capability check.
package router

import (
	"github.com/yi-nology/jumpserver-mcp-exec/biz/exec/handler"
	"github.com/yi-nology/jumpserver-mcp-exec/conf/exec"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/jmsapi"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/reg"
)

// Register puts every tool of the execution plane on the server.
//
// The REST client may be nil: the inventory tool needs it and the command
// tools do not, so a missing token disables one tool instead of the server.
func Register(r *reg.Registry, cfg conf.Config, c *jmsapi.Client) {
	handler.RegisterExecTools(r, cfg)
	if c != nil {
		handler.RegisterInventoryTools(r, c)
	}
	handler.RegisterTransferTools(r, cfg)
	// The trail tool needs nothing from the bastion: it reads this
	// machine's own files, so it works even when the REST client is
	// missing.
	handler.RegisterAuditTools(r, cfg.Audit)
}
