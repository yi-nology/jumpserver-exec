# swagger/ —— OpenAPI

`⚠️ **这一层是空的，而且它是故意空的。**`

这一面是 **MCP server**，不是 HTTP 服务：它没有自己的 REST API，它**调用**
堡垒机的 API。⇒ 它的「接口」是 MCP 工具，而那份契约在 `idl/`（thrift），
生成物在 `biz/model/`。

`⚠️ 什么时候它不再是空的：如果这一面开始自己提供 HTTP 接口（而不是通过
stdio），就在这里放生成的 OpenAPI，并让它与 `idl/` 同源。`
