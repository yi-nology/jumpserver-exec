# 变更记录

本文件遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
本项目遵循[语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### 变更

### 新增

### 修复

## [0.4.0] - 2026-11-04

### 变更

- ⚠️ **目录按 CloudWeGo hz 的标准骨架重排**：
  - **入口层搬到仓根**：`main.go` / `init.go` / `server.go` /
    `workers.go` / `shutdown.go` 是仓根那个 `package main`；
  - **`cmd/` 改成放子命令**：`cmd/serve`、`cmd/doctor`、`cmd/tools`
    是入口层 import 的**普通包**（不是各自的二进制）；
  - **`biz/` 去掉服务名前缀**：`biz/exec/` → `biz/`；
  - **数据访问层统一叫 `internal/repository/`**：
    `internal/exec/koko/` → `internal/repository/koko/`；
  - **配置挪到 `internal/config/`**（原 `conf/exec/`），
    包名 `conf` → `config`；
  - **风险表挪到 `internal/service/policy/`**（原 `biz/exec/policy/`）；
  - 补齐模板里的其余层（`pkg/`、`etc/`、`swagger/`、`deploy/`、
    `monitoring/`、`internal/middleware/`、`internal/rpchandler/`、
    `rpcx/`、`kitex_gen/`），**每一层都有自己的 README**，写着它为什么
    空、什么时候不再是空的。

- ⚠️ **`go install` 的路径变了**：
  `go install github.com/yi-nology/jumpserver-mcp-exec/cmd/jumpserver-mcp-exec@latest`
  ⇒ `go install github.com/yi-nology/jumpserver-mcp-exec@latest`。
  ⚠️ **旧路径不再存在** ⇒ 用旧命令装的人要改一次。
  release 页的二进制下载**不受影响**。

- ⚠️ **底座要求抬到 `jumpserver-mcp-kit v0.1.4`**：入口层的新布局要底座
  的守卫认（新判据 + 新增的「一个仓一个二进制」守卫）。
- 内部包名跟着改：`conf` → `config`（`config.Config`）；
  koko 那层包名不变（`koko`），只是搬到了 `internal/repository/koko/`。

## [0.3.3] - 2026-11-04

### 修复

- ⚠️ **`checksums.txt` 里的路径带目录前缀 ⇒ 校验形同虚设**：
  实测 `v0.3.2` 的 checksums 里写的是
  `./jumpserver-mcp-exec-linux-amd64/jumpserver-mcp-exec-linux-amd64`，
  而**下载页上的资产是扁平的** ⇒ 用户在下载目录跑
  `sha256sum -c checksums.txt` 会报「文件不存在」。
  ⇒ 算哈希时读源文件，只把输出里的路径换成 basename。

### 变更

- 版本号 0.3.2 → 0.3.3。

## [0.3.2] - 2026-11-04

### 修复

- ⚠️ **`v0.3.1` 装出来的二进制 `--version` 说「0.3.0」**：
  版本常量忘了跟着 tag 抬，而**它不在任何测试里、也不在任何守卫里** ⇒
  没人拦。`go install ...@v0.3.1` 装到的是功能正常、但版本号说错的二进制。
  ⇒ 版本常量抬到 `0.3.2`，并加一道守卫（见下）。

- **新增版本守卫**：二进制说的版本必须等于**它是从哪个 tag 构建出来的**
  （`version_test.go`）。
  ⇒ 在 **tag 触发**的 CI 里它有牙齿：忘了抬版本常量 ⇒ 发版构建当场红。
  ⇒ 在 main 上它**跳过**并说明原因 —— 否则「版本号先抬、tag 后打」这条正常
  流程会把自己的 CI 弄红，守卫就会被第一个被它烦到的人关掉。

### 变更

- ⚠️ **为什么是 `v0.3.2` 而不是重打 `v0.3.1`**：`v0.3.1` 已经被 Go 的模块代理
  （`proxy.golang.org`）与校验和库（`sum.golang.org`）**收录** ⇒
  **移动或删除那个 tag 会让已经记录了哈希的版本对不上** ⇒ 用户装不上。
  ⇒ 只能往前发一个新的。

- **新增 GitHub Release**（`.github/workflows/release.yml`）：tag 触发 ⇒
  六个平台（linux/darwin/windows × amd64/arm64）交叉编译 + `checksums.txt`。
  ⚠️ README 一直写着「方式二：release 二进制（见 Releases 页）」，
  但**从来没有发过 Release** ⇒ 照着做的人下到的��� 404。

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
