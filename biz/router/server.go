package router

// 这个文件是这一面的**装配**：把身份、风险表与工具目录接成一个能跑的
// server。
//
// ⚠️ 它住在 biz/router 而不是仓根，是因为**仓根那个 package 是 main** ——
// Go 不许 import 一个 main 包 ⇒ 仓根里的东西谁都用不了，而子命令
// （cmd/serve、cmd/tools）要用它。
//
// ⚠️ 身份里的**版本**由调用方递进来：版本是**二进制的属性**，而版本常量
// 住在仓根（package main）⇒ 子命令拿不到它 ⇒ 它只能从外面递进来。

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/service/policy"
	"github.com/yi-nology/jumpserver-kit/pkg/jmsapi"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
)

// ServerName is how this server calls itself — in the trail, in the MCP
// handshake, and in the catalogue.
//
// ⚠️ 它是**一个**名字，不是两个：留痕里写一个名字、握手里报另一个名字，
// 那两份日志就分不出谁是谁。
const ServerName = "jumpserver-exec"

// Options is what the caller may turn when it assembles the server.
type Options struct {
	// OnRecord receives one audit record per tool call.
	//
	// ⚠️ 只有**跑服务**那条路要留痕：「列工具目录」那种不碰堡垒机的命令
	// 不写留痕（留痕记的是「谁做了什么」，不是「有哪些工具」）。
	OnRecord func(reg.Record)
}

// New assembles this server: its identity, the risk side of the catalogue,
// and the tools themselves.
//
// ⚠️ 「哪些工具要确认、能不能撤销」是**一份**表（policy.Table），
// 「工具怎么实现」是**另一份**（handler）⇒ 两边必须一起装配：分开放，
// 就会有一份说「这个工具是只读的」、另一份说「它要改机器」。
func New(cfg config.Config, c *jmsapi.Client, version string, opts Options) *reg.Registry {
	r := reg.New(implementation(version), reg.Options{
		Policy:     policy.Table,
		ReadOnly:   cfg.ReadOnly,
		ServerName: ServerName,
		OnRecord:   opts.OnRecord,
	})
	Register(r, cfg, c)
	return r
}

// NewContract assembles the FULL catalogue for contract documents
// (swagger/openapi.json).
//
// ⚠️ 为什么不用 New(cfg, cfg.APIClient(), …)：`jmsx_inventory` 只在有 REST
// client 时才注册，而**签入的契约文档不能随生成它的机器变** —— 有凭据的
// 机器生成 15 个工具、没凭据的生成 14 个，文档就成了环境的函数。⇒ 文档永远
// 按完整目录（15 个工具）渲染。桩 client 只为让注册看到与部署相同的形状，
// 它永远不会被调用。
func NewContract(version string) *reg.Registry {
	cfg := config.Config{API: config.APIConfig{BaseURL: "http://127.0.0.1:1", Token: "contract-doc"}}
	return New(cfg, cfg.APIClient(), version, Options{})
}

// implementation says who this server is.
func implementation(version string) *mcp.Implementation {
	return &mcp.Implementation{Name: ServerName, Version: version}
}
