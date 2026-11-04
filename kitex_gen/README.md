# kitex_gen/ —— kitex 生成物

**⚠️ 这一层是空的，而且它是故意空的。**

两个服务共用的 kitex 生成物住在**底座仓**的 `kitex_gen/jmsrpc/`（从
`idl/jmsrpc/jmsrpc.thrift` 生成，`script/gen-rpc.sh`，产物入库）——泛型 RPC 面的
契约只有一份，生成物也就一份。

`⚠️ 什么时候它不再是空的：这一面开始有**服务专属**的 RPC 契约（`internal/rpchandler/`
的那个未来），就在这里放它自己的生成物（**入库**，与 `biz/model/` 同一规矩：
契约要能 checkout 出来就编译）。
`
