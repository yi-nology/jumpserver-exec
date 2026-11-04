# monitoring/ —— 监控

**⚠️ 这一层是空的，而且它是故意空的。**

这一面没有 exporter、没有指标端点：它是 stdio 上的一个进程，跑在 MCP 客户端
旁边。⇒ 它能被观测的**唯一**方式是它自己的留痕（JSONL，读回用
`jmsx_audit_query`）。

`⚠️ 什么时候它不再是空的：如果这一面开始常驻（而不是随客户端起落），就在
这里放 PrometheusServiceMonitor / Grafana 面板。`

## 这一面的对应物

执行面有 `jmsx_audit_query` 工具可以读回留痕；它自己也能用 `tail` 读那个 JSONL。
