package handler

import (
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
)

// RegisterTransferTools wires the transfer, batch and background tools. The
// approval memory reaches only the tools whose confirmation gate is
// conditional on the risk verdict (run, collect, collect_multi, exec_multi,
// bg_start) — the unconditional-confirm tools (put_file, put_dir, relay,
// relay_dir, bg_check's cleanup) never consult it, by design.
func RegisterTransferTools(r *reg.Registry, cfg config.Config, mem approvalMemory) {
	// ⚠️ Same backstop as RegisterExecTools: the approval memory is inert
	// in read-only mode — it may relax the confirmation gate, never the
	// read-only refusal.
	if cfg.ReadOnly {
		mem = nil
	}
	registerRunTool(r, cfg, mem)
	registerPutFileTool(r, cfg)
	registerPutDirTool(r, cfg)
	registerGetFileTool(r, cfg)
	registerGetDirTool(r, cfg)
	registerRelayTool(r, cfg)
	registerRelayDirTool(r, cfg)
	registerPoolStatusTool(r, cfg)
	registerCollectTool(r, cfg, mem)
	registerBgStartTool(r, cfg, mem)
	registerBgCheckTool(r, cfg)
	registerBgCancelTool(r, cfg)
	registerCollectMultiTool(r, cfg, mem)
	registerExecMultiTool(r, cfg, mem)
	registerSessionCostTool(r, cfg)
}
