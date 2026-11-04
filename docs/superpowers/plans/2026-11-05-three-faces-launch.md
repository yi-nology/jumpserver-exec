# 三张脸、一个启动模型 · 实施计划

> 执行方式：本会话内联执行（executing-plans）。Spec 已钉死契约与语义：
> `docs/superpowers/specs/2026-11-05-three-faces-launch-design.md`

**Goal:** 一个二进制三种用法——本地 MCP（stdio/HTTP）、远端 kitex RPC 服务、HTTP+RPC 常驻（`serve all`）。

**Architecture:** RPC 面是同一注册表的第三张脸：`reg.CallTool` 同一扇门（门禁/确认/留痕/schema 校验），拒绝是 is_error 合同答案、token 错误是传输错误。kit 承载共用机器（`idl/jmsrpc` + `kitex_gen/jmsrpc` 入库 + `pkg/rpcapi` + `pkg/faces` 唯一生命周期主人，落 `oekit/bootstrap` + `oekit/kitex.RunServer`），两服务对称接线。

**约束**（执行时逐条自检）：

- 构建 `GOPROXY=off`；唯一例外：加依赖用 `GOFLAGS=-mod=mod GOSUMDB=off GOPROXY=https://proxy.golang.org,direct go mod download all`（已实证）
- 禁裸 `go func()`（async.GoSafe；TestNoBareGoroutines 盯着）
- token 常量时间比较、逐调用、无匿名访问（含 Catalogue 带凭据）；`JMS_RPC_ADDR` 无 `JMS_RPC_TOKEN` ⇒ 启动即拒
- 拒绝是**答案**：unknown tool / 门禁拒绝 / 入参不合 schema → is_error 答复；token 错 → 传输错误（与 REST 401 同位）
- 留痕 caller=`rpc`；caller 自报可空（记 unnamed，不猜）
- kitex_gen 生成物入库；codegen 无 `-o` 旗标（kitex CLI v0.16.3），kit 根目录执行
- `CheckBaseSharedByBoth`：pkg/rpcapi + pkg/faces 必须两服务同时可达
- 版本收口：kit v0.5.0 → 两服务 v0.8.0（tag 后版本守卫复跑转绿）；go.work replace 同步

## Tasks

1. **kit · RPC 契约与依赖**：`idl/jmsrpc/jmsrpc.thrift`（Catalogue 带 token）→ `script/gen-rpc.sh` codegen → kitex_gen 入库；go.mod +kitex/oekit-kitex/oekit-bootstrap → `go mod download all`（真实代理）→ `GOPROXY=off go build ./...` 全绿 → arch 守卫复跑绿 → 提交
2. **kit · pkg/rpcapi**：TDD——先测后码。测试：token 正/误（误=传输错误）、unknown tool → is_error 答案、DANGEROUS 无 confirm → is_error 拒绝答案且留痕 caller=rpc、Catalogue 行数==注册数、text==render.ResultText 逐字节、Start/Stop 后端口可重绑。实现：`New(r, ServeOptions) (*Server, error)`（**New 内同步 bind**，失败即失败不留半启动）、`Svr()`、`RunBackground()`、`Stop()`；token 校验单一判据点 + constant-time；Invoke → reg.CallTool；unknown → is_error；text=ResultText；structured=Marshal(StructuredContent)（可选字段，失败降级为 is_error 答案）；Catalogue→reg.Catalogue() → ToolRow 映射
3. **kit · pkg/faces**：TDD——先测后码。测试：dispatch 表（""/"http"/"rpc"/"all" 各自的拒绝与放行矩阵）、`all` 没配任何面拒绝、`rpc`/`http` 没地址拒绝（示例端口提示：HTTP=ExampleAddr、RPC=JMS_RPC_ADDR 提示 JMS_RPC_ADDR 例）…（执行时定稿提示措辞）
   实现：`Options{HTTP *httpapi.ServeOptions, RPC *rpcapi.ServeOptions, Name, Stderr}`、`Run(ctx, r, opts, args)`——""→stdio r.Serve + rides（HTTP 现状 + RPC ride：New→RunBackground→defer Stop）；"http"/"rpc"/"all"→`bootstrap.New(WithServiceName, WithShutdownTimeout(5s))`→`Run(userFn)`：HTTP face=`httpapi.Start`+`AddResource("http", Shutdown)`、RPC face=`oekit/kitex.RunServer(app, name, s.Svr())`、`<-app.Context().Done()`。`httpapi.Run/serveUntilSignal` **搬**到 faces（不是拷贝）；httpapi 保留 New/Handler/Start/OpenAPI
4. **kit · 守卫与变异**：`script/mutate-rpc-face.py` 4 条变异全红（①摘 token 校验→auth 测试红 ②unknown tool 放行→unknown 测试红 ③text 不走 ResultText→red ④`all` 没地址不拒→red）；mutate-primitive-guards.py 里 2 条 serve 生命周期变异**迁**到新脚本（锚点改指 faces.go）；全测 + -race 绿
5. **kit · 发版收口**：CHANGELOG 0.5.0、AGENTS.md 模块清单、提交、tag v0.5.0；hashwork 算 kit v0.5.0 哈希 → cache + 两服务 go.sum 补行；两服务 go.work replace → v0.5.0
6. **exec · 服务侧**：config +JMS_RPC_ADDR/TOKEN（ADDR 无 TOKEN 拒）；cmd/serve → faces.Run 接线；`internal/rpchandler/`+`kitex_gen/` 空层 README 改写；CONFIGURATION/README/AGENTS/CHANGELOG 0.8.0
7. **mgmt · 对称**（与 6 同形）
6/7 后：两服务 build.sh 全绿 + openapi 零 diff
8. **两服务收口**：Version→0.8.0、提交、tag v0.8.0、版本守卫复跑绿
9. **发布与真调**：干净树重建+publish+配置指向（mcp.json 备份后改 command）→ kitex 客户端真调探针（/tmp，token 正/误、unknown、catalogue 行数、DANGEROUS 无 confirm 的 is_error 拒绝答案）→ doctor
10. **汇报**（verifier 待定：改动跨协议/失败路径 ⇒ 评审见最后一轮执行后决定）
