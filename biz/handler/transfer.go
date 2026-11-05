package handler

import (
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
)

func RegisterTransferTools(r *reg.Registry, cfg config.Config) {
	registerRunTool(r, cfg)
	registerPutFileTool(r, cfg)
	registerGetFileTool(r, cfg)
	registerRelayTool(r, cfg)
	registerBgStartTool(r, cfg)
	registerBgCheckTool(r, cfg)
	registerCollectTool(r, cfg)
	registerCollectMultiTool(r, cfg)
	registerExecMultiTool(r, cfg)
	registerPoolStatusTool(r, cfg)
	registerSessionCostTool(r, cfg)
}
