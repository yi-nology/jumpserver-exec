# 变更记录

本文件遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
本项目遵循[语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [0.16.0] - 2026-11-12
### 新增

- **K8s 配方：跳板机 kubectl 路径**（#30）：`skills/jms-exec-ops/references/
  k8s-kubectl.md`——K8s 平台资产开不出 shell，跳板机 kubectl 是接入路径
  （链路 2026-11-11 经 home-k8s-2 全链路实测全绿：exit 0、全输出、留痕在案；
  首调慢是链路抖不是坏，冷开实测 17.7s）。配方带巡检/排障的命令表与分类器
  对照（kit#28/#29/#31 落地后的档位，2026-11-12 对分类器实测）、多集群/多机
  跑法、SA token + view 级 RBAC 的凭据链配置法；SKILL.md 工具表与描述同步，
  `jmsx_reachable` 的 description 写明 K8s 资产答 reachable=false 不是绿灯
  （swagger 已随 `gen-openapi.sh` 再生成）。

### 修复

- **K8s 平台资产的会话在开出之前拒绝**（#29）：对 `platform=Kubernetes`
  的资产，连接令牌签得出、koko 也认令牌登录，但 koko 拒 exec 通道——命令
  类工具（exec / run / collect / exec_multi / collect_multi）拿到的是
  「exit 0 + 空输出」的假成功，agent 会误读成「机器上没有这个输出」。现在
  resolveAsset 的三个会话入口（自有连接、池连接、TUI 前检）都在签令牌与
  拨号之前按平台拒绝：错误带类型（`ErrK8sAssetNoShell`）与跳板机 kubectl
  的指引，`delivery=not_sent`（重试安全）。⚠️ 平台拒绝**不是**
  `ErrTokenUnavailable`——PTY 兜底对同一台资产只会漂移得更难读（同实测：
  有时打印集群报错，有时静默断），兜底不得接管；传输/后台工具走同一会话
  层，一并诚实拒绝。平台名大小写不敏感认 Kubernetes / k8s 缩写；空平台名
  （没解析出来）不拒——门在证据上拒绝，不在证据缺席上。
- **jmsx_reachable 对 K8s 资产报诚实不可达**（#31）：前置检查工具此前对
  K8s 资产报 `reachable=true` 的假绿灯（探测命令「跑成功」，输出其实是
  koko 的拒绝文案），agent 看到 true 就投整批任务、整批假成功。现在答复
  是 `reachable=false` + `error` 指引跳板机 kubectl + `delivery=not_sent`
  ——仍是工具的一次正常返回（前置检查的职责是报告，不是拒绝调用）。不另
  加 `shell_capable` 字段：reason 在 error 里说得清，契约不必为它长一格。

## [0.15.5] - 2026-11-11
### 变更

- **地板提升**：go 指令 1.26.0 → 1.27.0；`go get -u ./...` 全量升级
  （含底座 jumpserver-kit v0.12.4 → v0.12.5——带上 risk 门四个漏判修复），
  restore-go-modcache.sh 清单与 go.mod 逐版本同步（三方一致守卫盯着）。
  无破坏性升级：guardx/tools 随底座钉住的 v0.23.4 / v0.13.4 走（indirect）。

### 修复

- **commandTimeout 在乘法之前钳位**（#27）：`want` 是 64 位 int，
  `time.Duration(want) * time.Second` 回绕——9223372037 变负值击穿
  「max 600」钳位（负数不满足 `>`），轮询循环首次判断即超时，留痕却写
  「命令可能仍在机器上运行」；18446744074 回绕成 290ms 正值，钳位看着
  生效实际四分之一秒掐断。上限收进 commandTimeout 单点（乘法之前），
  run / collect / file 六处无自带钳位的调用点一并受益；exec.go 的旧钳位
  删除（已死代码）。jsonschema tag 无法承载 `maximum`（SDK 只读 description，
  `WORD=` 前缀直接报错），入参校验期拦截不可行，靠单点钳位兜底——守卫
  测试钉住溢出点两侧。
- **base64 传输路兑现三条自述保证**（#26）：照抄 channel 路已经做对的
  两步——`readlink -f` 先解析符号链接（写到链接的真身，`current ->
  release/v1.conf` 的发布布局不再被换成普通文件）；base64 解到目标的
  同级临时名后 `mv -f` 改名就位（同级=同文件系统=原子）；开场的 `rm -f`
  只清 /tmp 中转原料，**绝不预删目标**——传输中途失败留下的是完整的旧
  文件而不是空缺。守卫测试读假后端录下的命令序列：不含对目标的删除、
  含一次 sibling→target 的 mv、块失败/解码失败/改名失败都清理中转物。
## [0.15.0] - 2026-11-09
### 新增
- **issue 批收编第三轮**（ee67ddf + 增量修复）：`jmsx_bg_check` 加 `cleanup=true`
  （FINISHED 后就地清掉输出与退出标记，还在跑拒清，机器侧自证 REMOVED 才报
  cleaned）；`jmsx_put_dir` 加 `dry_run=true`（本地预扫 files/bytes/skipped，
  零会话成本）；`jmsx_relay_dir` note 补 bytes 语义；get_dir 流超时按远端树
  规模放大（`du -sk` 探测）；get_dir 失败回滚半棵树；执行面专用凭据槽
  `JUMPSERVER_EXEC_TOKEN` + doctor credential 行（#14）。
- **`jmsx_bg_check` 的 cleanup 要 `confirm=true`**（增量）：删机器上的文件是
  写——只读模式整个停用该工具（能删东西的工具不进只读目录），没带 confirm
  拒这次调用（拒在开会话之前，risk/blocked 随 BgCheckResult 走）。

### 变更
- **`jmsx_put_dir` 的流截止时间放大第一次真正生效**（增量）：上一版在 tar
  协程起跑后读流中计数——写端在无缓冲管道上阻塞在第一个条目上，计数恒
  ≈0。底数改用先行的 `PrescanDir` 预扫（与 dry_run 演练同源）。
- **get_dir 失败报告不再吞根因**（增量）：本地解包与远端流两侧都坏时
  `errors.Join` 两个都带上——超时（maybe_sent）与断链不再被
  unexpected EOF 拍平掉。

### 修复
- **`jmsx_bg_check` 恒报 RUNNING 的 P0**：探测命令把 `.rc` 后缀拼了两次，
  机器上探测的是永远不存在的 `p.rc.rc` ——后台任务的状态检查从上线起就没
  工作过（本仓与并行会话各自独立发现，ee67ddf 已修）。
- **PutDir 流失败泄漏 tar 协程**（增量）：RunInput 失败时关读端放写端出来，
  不再每次失败上传漏一个协程。
## [0.12.0] - 2026-11-06

### 新增

- **`jmsx_put_dir`：整目录上传**（工具数 15→16）。本地目录打成 tar 流、走执行
  通道的 stdin 一次传完（远端 `tar -xf - -C 目标目录`），不再是一个文件一次
  调用。语义是**合并**：同名覆盖、远端已有条目保留；要整树镜像先清目录再传。
  需要令牌路径与远端有 tar——TUI 屏幕路径如实报错，不降格成慢的假方案。

### 变更

- 底座抬 v0.8.1（网关 `ListSubQuery` 等）。

## [0.11.0] - 2026-11-05

### 变更

- **IDL 摊平**：`idl/jmsexec/` → `idl/`（单词汇仓不再多一层目录）。包名由全目录
  一致的 `namespace go jmsexec` 命名，入口仍是 `jmsexec.thrift`；生成物
  `biz/model/jmsexec/` 零变化，工具的契约形状与对外行为不变。
- `script/gen-idl.sh` 的入口推导改问文件一致声明的 `namespace go`，不再取目录名
  （平铺的 `idl/` 目录名不命名任何包）。

### 新增

- 契约命名守卫（底座 v0.8.0）认**两种布局**：平铺 `idl/`（单词汇仓）与嵌套
  `idl/<pkg>/`（多词汇仓），并首次有了仓内夹具测试（7 场景）与变异脚本
  `mutate-naming-guard.py`（6/6 变异被杀，边界登记在案）。

## [0.10.0] - 2026-11-05

### 变更

- **屏幕解析深度优化**（TUI 热路径，实测）：scanNumbered 每候选的三遍正则收为单遍
  子匹配索引、每行的三次 fmt.Sscanf 换 strconv.Atoi、行切分 Split→SplitSeq；
  每次全量扫描的分配 35940 → 16862（-53%），耗时约 3 倍速。新增
  screen_bench_test.go 基准钉住 per-scan 成本。
- 危险命令分类随底座 v0.7.0 变严：`sudo -n rm -rf /x`、管道后半、`$(...)` 里的
  rm 现在判 DANGEROUS 并要求确认——此前这些是 MODIFY，门禁无声。日常命令探针零误伤。
- 资产解析随底座改翻页：分页部署上窗口外的资产不再被报成"不存在"。

### 修复

- 翻页中途失败随底座改报错（曾是静默截断的"全量"）。

## [0.9.0] - 2026-11-05

### 修复

- **连接池 TTL 报告失真**：`jmsx_pool_status` 与 `--doctor` 此前报编译期常量 2m，
  实际生效值来自 `JMS_POOL_TTL`（新增 `Pool.TTL()`）；操作员改配置后不再读到旧数字。
- **无连接池部署丢失 TUI 回退**：`openOwnConnection` 对 koko 拒签令牌登录的错误
  现在与 `dialPooled` 一样包上 `ErrTokenUnavailable`——同一拒绝，有无连接池行为一致。
- `jmsx_pool_status` 恒假的 `Backend == nil` 判断移除；TUI 握手失败文案
  "after 5 confirms" 更正为 6。

### 变更

- **判定词汇收编为单一写法**：六个工具的演练/被拒答复改用底座的
  `render.FillRehearsal`/`FillRefusal`（见 kit v0.6.0），字段拼法不再各写各份。
- **worker 池统一为 `runSweep`**：`jmsx_exec_multi` 内联的 jobs/GoSafe/WaitGroup 池与
  `runAcrossAssets` 合并为一个带显式选项（stop_on_error / feed 预算 / 空资产跳过）的
  `runSweep`；顺带移除只写不读的 `firstErr` 死标志。
- **配置装载走底座 `confload`**：三段式读档、HTTP/RPC 拒绝文案、密码/OTP 回退收编。
- **audit limit 复述走底座 `audit.EffectiveLimit`**，本地副本删除。
- **大文件拆分（行为不变）**：koko.go（1467→883 行）拆出 screen.go（屏幕解析纯函数）；
  token.go（1284→839）拆出 mint.go（铸 token 与错误分类）；biz/handler/transfer.go
  （1100 行）按域拆为 run/file/bg/collect/sweep/pool 六个文件 + 调度器。
- 死代码清理约 120 行（不可达分支、零调用函数与字段、纯转发包装）。

## [0.8.0] - 2026-11-05

### 新增

- **kitex RPC 面（第三张脸）**：其他服务可经 thrift 同步调用本面的工具——
  `JMS_RPC_ADDR`（kitex 监听地址）+ `JMS_RPC_TOKEN`（逐调用凭据；ADDR 配了
  TOKEN 缺 ⇒ 启动即拒，与 HTTP 面同一纪律：没有「不开认证」模式）。调用语义与
  stdio/REST 逐字同构：同一道门禁、同一套确认、同一份留痕（caller=`rpc`），
  拒绝与失败是 is_error **答案**不是传输错误。契约在底座（kit 的
  `idl/jmsrpc` + `kitex_gen/jmsrpc`），生命周期在底座 `pkg/faces`。
- **启动模型**（`serve` 子命令）：`serve`（无参）= stdio MCP + 配置了地址的面
  ride-along；`serve http` = 只 HTTP；**`serve rpc` = 只 kitex**（新）；
  **`serve all` = HTTP+RPC 常驻**（新；两者都没配拒绝启动；不含 stdio——常驻
  进程的管道那头没人）。常驻模式组合在 `oekit/bootstrap` 上（信号、逐资源
  关闭预算）。

## [0.7.0] - 2026-11-04

### 变更

- **列表样板收编到底座 `render.ListSpec`**（`jmsx_inventory` 的资产信封）：列表信封的
  形状只有一份（filter → map → sort → truncate → count，`total` = 匹配数，截断才报
  `truncated`）。
- **演练收编**：「这命令会不会跑、缺什么」的判定与措辞收编到 `risk.Rehearsal`（两个面
  唯一源）—— `blockedCommand` / `rehearsal` 不再各自拼那句话；本面措辞逐字不变。
- **入口层收编**：`cmd/serve/http.go` 删除，serve 生命周期（listen / 后台 serve / 信号 /
  5s 优雅关闭）交给底座 `httpapi.Run`；`cmd/tools` 的表格循环交给 `reg.CatalogueTable`；
  `openapi_test.go` 缩为一次 `testsupport.CheckOpenAPI`（守卫身两仓一份）。
- **日志措辞统一到共用生命周期**（行为变化）：stdio 失败行
  `jumpserver-exec: server failed: …` → `jumpserver-exec: serve: …`；`serve http` 缺地址
  的提示改为 `serve http needs an address to listen on — JMS_HTTP_ADDR, …`；HTTP 面
  后台失败行不再带服务前缀（`http: …`）。
- **底座升 `jumpserver-kit` v0.4.0**。

### 修复

- **`jmsx_inventory` 的计数口径**（收编时暴露）：`total` 原先报**未过滤**的行数（现在 =
  匹配数）；`truncated` 原先「被过滤掉」即为真 —— 这个列表从不截断，现在恒 `false`。

## [0.6.0] - 2026-11-04

### 变更

### 新增

- **HTTP API 面（REST + MCP over streamable HTTP）**：同一份工具目录的第二个面，
  与 stdio **共用同一个注册表** ⇒ 同一道风险门禁、同一套确认语义、同一份留痕
  （HTTP 面不是后门：stdio 拒的，这里一样拒）：
  - 路由：`GET /api/v1/health`（**唯一**免认证，只报名字 / 版本 / 只读与否 / 工具数）、
    `GET /api/v1/tools`（目录：风险、确认要求、入参 / 出参形状）、
    `POST /api/v1/tools/{name}/invoke`、`/mcp`（MCP over streamable HTTP）。
  - 认证：Bearer 令牌（`JMS_HTTP_TOKEN`，常量时间比较）；**没有「不开认证」模式**
    （`JMS_HTTP_ADDR` 有、`JMS_HTTP_TOKEN` 无 ⇒ 启动即拒）。
  - **结果的读法与 stdio 逐字相同**：拒绝、失败、入参不合形状一律 HTTP 200 +
    `is_error=true`；请求本身坏才 400，没有这个工具才 404。
  - 两种跑法：`serve`（默认，stdio）配了 `JMS_HTTP_ADDR` **顺带**开 HTTP；
    `serve http` 只开 HTTP，长跑到信号（当服务部署用这个）。
  - 留痕的 `client` 列记 **`rest`**（HTTP 面认证的是令牌不是人，不记「alice」）。
- **OpenAPI 契约**：`swagger/openapi.json` 由 `tools openapi` 生成
  （`./script/gen-openapi.sh`，产物入库，生成不需要任何秘密），与 MCP 工具目录**同源**
  （入参 = Go 类型派生，出参 = `idl/` 契约生成物）。⚠️ 契约文档**恒渲染完整 15 个工具**
  （`router.NewContract` 桩 client）—— `jmsx_inventory` 注册与否看 REST 凭据，
  而签入的契约不能随生成机器的环境变（`tools` 列表的行为不变，仍按实际注册数）。
  守卫 `openapi_test.go` 拿签入的文档与再生成的**逐字节**比对（含版本号）；
  「这道守卫会红」由 `script/mutate-swagger-guard.py` 机械证明（对照 + canary + 4 条变异全红）。
- 新环境变量 `JMS_HTTP_ADDR` / `JMS_HTTP_TOKEN`（见 docs/CONFIGURATION.md §3.9）。
- **底座升 `jumpserver-kit` v0.3.0**：`pkg/httpapi`（REST + MCP over HTTP，两面共用
  同一注册表）、`pkg/reg` 的 schema 捕获与 `CallTool` / `StreamableHTTPHandler`。

### 修复

## [0.5.0] - 2026-11-04

### ⚠️ 更名（breaking）

- 项目名去掉 `mcp`（本服务不止 MCP——接下来同时提供 HTTP API）：
  - **`jumpserver-mcp-exec` → `jumpserver-exec`**（本仓）
  - `jumpserver-mcp` → `jumpserver-mgmt`
  - 底座 `jumpserver-mcp-kit` → `jumpserver-kit`（**v0.2.0**，模块路径
    `github.com/yi-nology/jumpserver-kit`）
- 接线影响：`go install` 路径变为 `github.com/yi-nology/jumpserver-exec@latest`，
  二进制名 `jumpserver-exec`；本机 MCP 配置里的 `command` 指向的运行时目录变为
  `~/.local/jumpserver-exec-runtime/<时间戳>/`（发布脚本同一套规矩）。
- GitHub 仓名同步更名（旧 URL 自动跳转）；历史条目里的旧名已统一改写为新名，
  旧 → 新映射以本条为准。
- 本次只动名字，工具面（15 个工具）与行为不变。

## [0.4.4] - 2026-11-04

### 修复

- **`--version` 报的版本号抬到 0.4.4**：v0.4.3 发版时 `main.go` 的 `Version`
  常量忘了跟着抬（仍报 0.4.2）⇒ 被 `TestTheVersionMatchesTheTagItWasBuiltFrom`
  逮住。⚠️ v0.4.3 的 tag 已推送、不可移动 ⇒ 这一版补发；装了 v0.4.3 的用户
  `--version` 会看到 0.4.2，以本版为准。

## [0.4.3] - 2026-11-04

### 变更

- **底座抬到 v0.1.9**：凭据守卫第二轮补齐（独立审阅的 3 条跟进项 + 漏报）：
  - **豁免标记改锚定识别**：散文里**提到**标记的行不再豁免下一行（文档不再
    自造盲区）；裸标记行（无注释前缀）也不是标记。整行标记豁免下一行、
    行尾注释标记豁免当行。
  - **补漏报形态**：Go 短声明 `:=`、URL 内嵌凭据、URL 查询串参数（JWT 形态）、
    空白分隔（`.netrc`）、无后缀文件的 PEM 嗅探（`id_rsa`）、已知名字变体
    （`Dockerfile.prod`）。缩写键名改分隔符边界后缀（`MY_TOK`/`api-tok` 命中）。
  - **URL 值判定收紧**（URL 凭据字母表）⇒ 守卫扫自己仓零误报。
  - 变异 12→24 条，自证 24/24 全被测试杀掉。

### 新增

### 修复

## [0.4.2] - 2026-11-04

### 变更

- **底座抬到 v0.1.8**：上一版（v0.1.6）的凭据守卫**抓不住它要防的那件事**
  —— 独立审阅把归档仓历史里**真实带令牌的那几个文件**原样喂给守卫，结果是
  **0 命中**。⇒ v0.1.8 补齐了判据：缩写键名（`TOK`）、位置参数
  （`env.setdefault("K", "…")`）、shell 默认值（${K:-…}`）、JSON
  成员，以及**非十六进制**的凭据（base64 形态的密钥 / GitHub PAT / AWS secret）。
  ⚠️ 缩写键名用的是守卫自己的**窄名单**，刻意**没有**进 `pkg/redact` 的词表 ——
  那张表决定留痕记什么、答复掩什么，加词会改变另外两条路径的行为。

### 新增

### 修复

## [0.4.1] - 2026-11-04

### 变更

- **底座抬到 v0.1.6**（多了明文凭据那条守卫的机械）。

### 新增

- ⚠️ **明文凭据不许进仓**（守卫体在底座 `pkg/arch/credential.go`，本仓的入口是
  `internal/arch/execution_test.go` 里那条
  `TestThePlaintextCredentialsAreNotInTheRepository`）：
  - **动机**：本机一个未推送远端的归档仓，其 git 历史里明文躺着一枚堡垒机
    **admin 用户的 PrivateToken**。三个公开仓全历史 **0 命中**（公网没有它），
    但它是被人**手工翻历史**才发现的 —— 构建抓不到它，因为脚本里的凭据是
    一个完全合法的程序。
  - **判据是「形状 + 语境」两半**（形状说「这可能是秘密」，语境说「它本来
    就是一个凭据」）⇒ `.github/workflows/` 里每个 commit SHA pin 不会被误伤。
  - **行级显式豁免**：`jms:allow-credential` ⇒ 豁免当行，且**每一处豁免
    都被守卫列出来**。
  - ⚠️ 执行面自己**不接**任何凭据参数 ⇒ 这里守的不是「参数要脱敏」，而是
    「仓里不许躺着一个能用的凭据」。

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
  `go install github.com/yi-nology/jumpserver-exec/cmd/jumpserver-exec@latest`
  ⇒ `go install github.com/yi-nology/jumpserver-exec@latest`。
  ⚠️ **旧路径不再存在** ⇒ 用旧命令装的人要改一次。
  release 页的二进制下载**不受影响**。

- ⚠️ **底座要求抬到 `jumpserver-kit v0.1.4`**：入口层的新布局要底座
  的守卫认（新判据 + 新增的「一个仓一个二进制」守卫）。
- 版本号 0.3.3 → 0.4.0（目录重排是**破坏性**改动 ⇒ 抬 minor；
  旧 tag 不可移动，所以只能往前发）。
- 内部包名跟着改：`conf` → `config`（`config.Config`）；
  koko 那层包名不变（`koko`），只是搬到了 `internal/repository/koko/`。

## [0.3.3] - 2026-11-04

### 修复

- ⚠️ **`checksums.txt` 里的路径带目录前缀 ⇒ 校验形同虚设**：
  实测 `v0.3.2` 的 checksums 里写的是
  `./jumpserver-exec-linux-amd64/jumpserver-exec-linux-amd64`，
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

- **底座版本抬到 `jumpserver-kit v0.1.1`**：那一版修掉了
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
  在 [jumpserver-kit](https://github.com/yi-nology/jumpserver-kit) 仓。

### 变更

- ⚠️ **改名**：本项目从单体仓 `jms-mcp` 拆成独立仓 `jumpserver-exec`。
  - 二进制：`jumpserver-exec`（原来是 `jms-exec-mcp`）。
  - MCP 服务名：客户端配置里的 key 默认为 `jumpserver-exec`（部署时可改）；
    运行时目录默认为 `~/.local/jumpserver-exec-runtime/<时间戳>/`。
  - ⚠️ **留痕里 `service` 字段的值变了**：老留痕里写的是 `jms-exec-mcp`，
    现在写的是 `jumpserver-exec` ⇒ **按服务过滤时，老留痕默认匹配不上**
    （`jmsx_audit_query` 的 `service` 过滤按**新名字**筛）。
    ⇒ 查历史记录时，要么把过滤条件改成 `jms-exec-mcp`，
    要么先按时间范围捞出来再看。留痕文件本身在同一个目录里
    （目录名从 `~/.local/state/jms-mcp/audit` 改为 `~/.local/state/jumpserver-mgmt/audit`，
    见 kit 仓的 CHANGELOG）。
