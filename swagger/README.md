# swagger/ —— HTTP 面的 OpenAPI

`swagger/openapi.json` 是 **HTTP 面**（REST `/api/v1/…` + MCP over
streamable HTTP `/mcp`）的 OpenAPI 3.1 文档。它是**生成物**，与 MCP
工具目录**同源**：同一个注册表、同一份 schema —— 入参形状来自 Go 类型
（SDK 派生），出参形状来自 `idl/` 契约（thrift 生成物）。

- **重新生成**：`./script/gen-openapi.sh`（跑 `tools openapi`，产物入库；
  生成不需要堡垒机、不需要任何秘密 —— 契约用桩 client 渲染**完整目录**
  （15 个工具），文档不随生成机器的凭据变）。生成是确定性的：同一个工具
  目录渲染出同样的字节。
- **守卫**：仓根的 `openapi_test.go` 拿签入的文档与再生成的字节比对
  ⇒ 改了工具、契约或版本而没重新生成，测试就红。守卫会红这件事有
  机械证明：`script/mutate-swagger-guard.py`（对照 / canary / 4 条变异）。

⚠️ **什么时候要重新生成**：改 `idl/`（先 `./script/gen-idl.sh`）、改工具的
入参结构体、增删工具、改风险表/确认话术、抬 `main.go` 的 `Version`。
最后一条是最容易忘的：版本抬了文档没跟上，签入的契约就文档的是**上一版**。

## 这一面的对应物

这一面**调用**堡垒机的 API 与 koko TUI（`internal/repository/koko/`），
它自己的接口是 MCP 工具（`idl/`）+ HTTP 面（本目录）。两份契约出自同一份
注册表，手写任何一份都会漂 —— 所以都不手写。
