# 三张脸、一个启动模型 —— MCP(stdio/HTTP) + kitex RPC + all

- 日期：2026-11-05
- 状态：**已批准**（用户确认三个分岔：两服务都上 RPC 面 / 泛型 Invoke / `serve all` 不含 stdio；并指令用 oekit 生态深度优化——设计落在 `oekit/bootstrap` 组合根 + `oekit/kitex RunServer` 上）
- 范围：kit v0.5.0 → mgmt v0.8.0 / exec v0.8.0

## 1. 背景与目标

执行面/管理面今天有两张脸：stdio MCP（本地）与 HTTP 面（`pkg/httpapi`：REST + MCP
over streamable HTTP，部署形态）。架构文档早预留了第三个槽位：
`internal/rpchandler/` 的 README 明写「别的服务开始同步调用时，RPC 服务端放这里」，
`kitex_gen/` 的 README 明写「开始用 kitex 就放生成物（入库）」。

目标：一个二进制、三种用法——本地 MCP、远端 RPC 服务（kitex，供其他服务同步调用）、
以及两者并存的常驻形态。用户对「MCP http+stdio 的合理性」的判断请求：**保留现状**——
同一注册表、同一道门禁、同一份留痕的两个传输，本来就该有。

「hz」不引入：HTTP 面已是 `pkg/httpapi`（net/http），再引 hz 代码生成 = 第二套
HTTP 栈 = 分裂。「hz/kitex」里落地的是 kitex。

## 2. 启动模型（子命令 + 环境变量，与现状同风格）

| 子命令 | 跑什么 | 状态 |
|---|---|---|
| `serve`（无参） | stdio MCP；配了 `JMS_HTTP_ADDR` 顺带 HTTP、配了 `JMS_RPC_ADDR` 顺带 RPC | 现状 + RPC ride |
| `serve http` | 只 HTTP 面 | 环境变量不变 |
| `serve rpc` | 只 kitex RPC 面 | **新** |
| `serve all` | HTTP + RPC 常驻（信号→逐资源优雅关闭；两者都没配则拒绝启动；**不含 stdio**——常驻进程的管道那头没人） | **新** |

新环境变量：`JMS_RPC_ADDR`（kitex 监听地址，空 = 无 RPC 面）；
`JMS_RPC_TOKEN`（**ADDR 配了 TOKEN 缺 ⇒ 启动即拒**，与 HTTP 面同一纪律：没有
「不开认证」模式）。

## 3. kit 侧

### 3.1 RPC 契约（idl/jmsrpc → kitex_gen/jmsrpc，生成物入库）

```thrift
struct ToolCall {
  1: required string tool,
  2: required binary args,      // JSON object 字节，与 REST body 同形
  3: required string token,     // 逐调用 Bearer 等价物
  4: optional string caller,    // 留痕的自报姓名；缺省记 unnamed
}
struct InvokeResult {
  1: required bool is_error,
  2: required string text,      // render.ResultText
  4: optional binary structured // structured content 的 JSON 字节
}
struct ToolRow { … name/description/risk/confirm/input_schema/output_schema … }
service ExecFace {
  InvokeResult Invoke(1: ToolCall call)
  list<ToolRow> Catalogue()
}
```

工具契约唯一源不变：RPC 答复携带 `text`（`render.ResultText`）+ `structured`
（JSON 字节）——与 REST 信封同构，不出现第三份入参形状。15/97 个工具的 MCP schema
仍是唯一契约。

### 3.2 pkg/rpcapi（kitex 面）

- `RPCCaller = "rpc"`（留痕 caller 标签，与 `httpapi.RestCaller` 同位）
- `New(r *reg.Registry, opts Options) (*Server, error)`：装配 kitex server（未启动）；
  `Invoke` → 逐调用 **constant-time** token 校验（单一判据点）→ `reg.CallTool(ctx,
  RPCCaller, tool, args)`（同一扇门：门禁、确认、留痕、schema 校验全在下面）；
  `Catalogue` → `reg.Catalogue()`（目录唯一装配）；unknown tool → **is_error 答案**
  （拒绝/失败是答案不是传输错误——与 stdio/REST 同纪律）
- 生命周期交给 `oekit/kitex.RunServer(app, name, svr)`（组合根里调）：
  AddResource(Stop) + 后台 Run + panic→RequestShutdown，探活与注册门由 bootstrap 的
  就绪门承接（本轮无注册中心，不配 OnReady）

### 3.3 pkg/faces（唯一生命周期主人）

`httpapi.Run` 的 serve 生命周期**搬**到这里（不是拷贝——两份会漂）：

- `Options{HTTP *httpapi.ServeOptions, RPC *rpcapi.ServeOptions, Name, Stderr}`
- `Run(ctx, reg, opts, args)`：
  - `""` → stdio `r.Serve(ctx)` + rides（HTTP 现状；RPC ride 新增：后台起 kitex、
    退出时 Stop）
  - `http` / `rpc` / `all` → `oekit/bootstrap.New(WithServiceName, WithShutdownTimeout(5s))`
    → `Run(userFn)`：起 face(s)（HTTP：`httpapi.Start` + `AddResource("http", Shutdown)`；
    RPC：`oekit/kitex.RunServer`）→ `<-app.Context().Done()`。bootstrap 自带
    SIGINT/SIGTERM 监听与逐资源超时预算的关闭序
  - `all` 且 HTTP/RPC 都没配 ⇒ 拒绝启动
- `httpapi` 保留 `Options/New/Handler/Start/OpenAPI`（faces 与 REST 面共用）

### 3.4 依赖与守卫影响

- kit go.mod += `oekit/kitex v0.7.6`、`oekit/bootstrap v0.6.8`（均在 module cache）
- `CheckBaseSharedByBoth`：pkg/rpcapi + pkg/faces 必须两服务同时可达 ⇒ 两服务都接线
  （已确认的决策）；kitex 生成物在 kit，服务经 kit 传递依赖
- go.sum / restore 脚本同步 kitex+bootstrap 依赖树（钉版守卫 `CheckDependencyPinning`
  自动盯）

## 4. 服务侧（mgmt 与 exec 对称）

1. `cmd/serve`：`faces.Run` 接线（HTTP/RPC ServeOptions or nil；ExampleAddr 保留）
2. `internal/config`：`JMS_RPC_ADDR` / `JMS_RPC_TOKEN` 字段 + 校验（ADDR 无 TOKEN 拒）
3. 空层 README 改写：`internal/rpchandler/`（「泛型面在 kit 的 pkg/rpcapi；这层留给
   未来不适合泛型 Invoke 的服务专属 RPC 方法」）、`kitex_gen/`（「生成物在 kit 的
   kitex_gen/jmsrpc」）
4. 文档：`docs/CONFIGURATION.md`（新环境变量、四种用法、部署形态）、README、
   AGENTS.md、CHANGELOG（0.8.0）
5. `swagger/openapi.json` 不变（REST 面没动；重新生成应零 diff）

## 5. 验证链

- kit：build / 全测 / -race；`rpcapi_test`（token 正/误、unknown tool→is_error、
  catalog、Invoke 走门禁——被拒的写工具在 RPC 面同样被拒且留痕 caller=rpc、无裸
  goroutine）；`faces_test`（四子命令 dispatch、all 没配拒绝、优雅关闭）
- **变异脚本 `mutate-rpc-face.py`**（≥4 条，全红才算过）：① 摘 token 校验 → auth
  测试红；② unknown tool 返回成功 → 红合同测试；③ text 不走 `render.ResultText` →
  红；④ `serve all` 没地址不拒 → 红
- 两服务：build.sh 全绿、openapi 零 diff、doctor、干净树发布（新时间戳目录）+
  **kitex 客户端真调**（/tmp 探针经 `kitex_gen/jmsrpc` 客户端：token 正/误、
  unknown tool、catalogue、DANGEROUS 无 confirm → is_error 拒绝答案）
- 版本收口：kit tag v0.5.0 → 两服务 tag v0.8.0（版本守卫复跑转绿）；go.work replace
  同步

## 6. 明确不做

- hz HTTP 代码生成（第二套 HTTP 栈）
- `rpcx/` 保持空——本轮不做消费方（mgmt 调 exec 的 RPC 是后续项）
- 类型化 15 方法（第三份入参形状会漂；无具体类型化消费者）
- 重试语义与投递状态纪律不动（通用 retry 库不适用）
- stdio 模式的信号语义不动（现网验证过）
