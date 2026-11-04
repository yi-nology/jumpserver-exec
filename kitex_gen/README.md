# kitex_gen/ —— kitex 生成的代码

`⚠️ **这一层是空的，而且它是故意空的。**`

这一面不用 kitex（那是 CloudWeGo 的 RPC 框架）：它的传输是 stdio 上的 MCP，
契约是 **thrift**，生成物在 `biz/model/`（由 `script/gen-idl.sh` 从
`idl/` 生成，产物**入库**）。

`⚠️ 什么时候它不再是空的：如果这一面开始用 kitex，就在这里放生成物
（**入库**，与 `biz/model/` 同一规矩：契约要能被 checkout 出来就编译）。`
