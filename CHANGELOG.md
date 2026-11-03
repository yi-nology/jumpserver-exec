# 变更记录

本文件遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
本项目遵循[语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### 变更

### 新增

### 修复

## [0.3.1] - 2026-11-04

### 修复

- **发布自检不再把「缺配置」误判成「二进制坏了」**
  （`script/publish-mcp.py`）：以前「进程退出」一律判 FAIL ⇒ 每次发布都报一个
  假红，而真坏的时候反而没人看了。
  ⇒ 现在：它**自己说了原因**地退出 ⇒ 判 OK（并把原因带出来）；
  没说话就退出（被系统杀掉 / 段错误）⇒ 才是真的起不来；
  panic 仍判 FAIL（它的话里有 `panic:` 与 goroutine 栈 ⇒ 那是崩了）。

### 变更

- **底座版本抬到 `jumpserver-mcp-kit v0.1.1`**：那一版修掉了
  「三条底座守卫其实从未执行过」的问题（`protocolLayerFile` 拿带 `.go` 的
  文件名去比导入路径 ⇒ 永不相等 ⇒ 状态词汇 / 能力监视名单 / 发行版词汇
  三条检查一条都没跑过，而守卫仍报绿）。
  ⚠️ 本仓的守卫住在底座里 ⇒ 底座发了新 tag，本仓**必须**跟着抬 require。

## [0.3.0] - 2026-11-04

首个发布。

### 新增

- **15 个工具**（没有 REST 凭据时是 **14** —— `jmsx_inventory` 只在有 API client 时注册）：
  单命令执行（`jmsx_exec`）、一次会话多条命令（`jmsx_run`）、
  采集事实（`jmsx_collect`）、多机执行与多机采集（`jmsx_exec_multi` /
  `jmsx_collect_multi`）、连通性（`jmsx_reachable`）、
  后台长任务（`jmsx_bg_start` / `jmsx_bg_check`）、
  文件传输与中转（`jmsx_put_file` / `jmsx_get_file` / `jmsx_relay`）、
  会话成本与连接池（`jmsx_session_cost` / `jmsx_pool_status`）、
  资产清单（`jmsx_inventory`）、留痕查询（`jmsx_audit_query`）。
- **确认门禁**：危险命令（真 shell AST 判定）要 `confirm=true`，
  与管理面同一标准、**同一个分类器**；写远端文件一律要确认。
- **投递状态**：每次动作都带 `not_sent` / `maybe_sent` ⇒
  **失败之后能回答「它还在那台机器上跑着吗」** ⇒ `maybe_sent` 的命令**永不重试**。
- **后台任务**：`jmsx_bg_start` 用 `setsid` 脱离会话，退出码写在 `.rc` 标记文件里
  —— 靠它区分「还在跑」和「跑完了」。
- **文件传输**：走 exec 通道的 stdin/stdout（不落本地磁盘），远端取段（`offset`/`length`）。
- **留痕查询**（`jmsx_audit_query`）：可按服务、工具、客户端、放行与否、
  **失败**、**投递状态**、时间筛。
- **共用底座**：协议层、门禁、输出契约、留痕、脱敏、风险分类、失败分类、共用词汇 ——
  在 [jumpserver-mcp-kit](https://github.com/yi-nology/jumpserver-mcp-kit) 仓。

### 变更

- ⚠️ **改名**：本项目从单体仓 `jms-mcp` 拆成独立仓 `jumpserver-mcp-exec`。
  - 二进制：`jumpserver-mcp-exec`（原来是 `jms-exec-mcp`）。
  - MCP 服务名：客户端配置里的 key 默认为 `jumpserver-exec`（部署时可改）；
    运行时目录默认为 `~/.local/jumpserver-mcp-exec-runtime/<时间戳>/`。
  - ⚠️ **留痕里 `service` 字段的值变了**：老留痕里写的是 `jms-exec-mcp`，
    现在写的是 `jumpserver-mcp-exec` ⇒ **按服务过滤时，老留痕默认匹配不上**
    （`jmsx_audit_query` 的 `service` 过滤按**新名字**筛）。
    ⇒ 查历史记录时，要么把过滤条件改成 `jms-exec-mcp`，
    要么先按时间范围捞出来再看。留痕文件本身在同一个目录里
    （目录名从 `~/.local/state/jms-mcp/audit` 改为 `~/.local/state/jumpserver-mcp/audit`，
    见 kit 仓的 CHANGELOG）。
