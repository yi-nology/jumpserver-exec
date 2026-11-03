# JumpServer MCP 执行面配置指南

执行面 MCP 服务（`jumpserver-mcp-exec`）的配置说明：
怎么接线、每个环境变量是什么、怎么自检、出问题怎么查。

> 本文面向**要装、要调、要修**的人。只想知道「用哪个工具」看
> [§5 工具选择速查](#5-工具选择速查)；只想跑起来，看 [§2 五分钟装好](#2-五分钟装好)。
>
> **管理面**（另一个服务）的配置在
> [jumpserver-mcp 仓的 CONFIGURATION.md](https://github.com/yi-nology/jumpserver-mcp/blob/main/docs/CONFIGURATION.md)。

---

## 1. 架构：执行面

```
       ┌──────────────────┐         ┌──────────────────┐
       │  jumpserver-mcp  │  REST   │  JumpServer 部署  │
       │  (管理面, 97 工具) ├────────►│                  │
       └──────────────────┘         └────────┬─────────┘
                                             │ koko :2222
       ┌──────────────────┐         ┌────────┴─────────┐
       │ jumpserver-mcp-  │  SSH    │  受管机器          │
       │ exec (本服务,    ├────────►│  (录像可审计)      │
       │ 15 工具)          │         │                   │
       └──────────────────┘         └───────────────────┘
```

**铁律：AI 访问机器一律经堡垒机 koko(2222)，不接受直连 SSH。** 直连会丢审计录像。

| 服务 | 管什么 | 不管什么 |
|---|---|---|
| `jumpserver-mcp`（管理面） | 资产、账号、权限、标签、命令过滤、审计、授权 | 不在机器上执行命令 |
| `jumpserver-mcp-exec`（本服务） | 在机器上跑命令、传文件 | 不改堡垒机配置 |

执行面也读 `JUMPSERVER_URL` + `JUMPSERVER_TOKEN`（用来换连接令牌、列资产），
并且**共用同一份 REST 协议实现**（在
[jumpserver-mcp-kit](https://github.com/yi-nology/jumpserver-mcp-kit) 仓：
传输、重试、限流、v5 的两种列表形状、**端点表**）。
协议只有一份，所以在管理面修好的东西（比如按字符边界截断、避免切坏多字节字符）
执行面自动就有。

端点表也在同一处：`jmsapi.ResPermissions` 这样的常量**就是**堡垒机的 URL。
工具层只说「我要权限列表」，不说「GET /perms/asset-permissions/」——
所以改端点只改一个文件，写错路径编译器当场报错，机器上也跑不出 404。

---

## 2. 五分钟装好

### 2.1 前置条件（堡垒机侧）

| 需要的东西 | 怎么拿 | 缺了会怎样 |
|---|---|---|
| JumpServer 部署地址 | 你自己的部署，例如 `https://jumpserver.example.com`（`JUMPSERVER_URL`，**无默认值**） | 执行面只丢连接令牌那条路（退回 koko 终端）；管理面直接启动失败 |
| 一个 JumpServer 用户 | 建议单独建 `ai` 账号，别复用真人账号 | koko 登录失败 |
| 该用户的 **RSA** SSH 公钥 | 见下方 2.2 | koko 拒绝登录 |
| 给 `ai` 的资产授权 | `accounts` 必须是 `@ALL` 或显式账号 | 登录报「没有可用的授权账号或终端协议」 |
| PrivateToken | 堡垒机 UI → 个人设置 → API 密钥 → 新建 | 资产清单工具（`jmsx_inventory`）停用；连接令牌那条路也停用 |

### 2.2 注册 SSH 公钥（最容易踩的一步）

koko（2222）**只接受 `ssh-rsa`**。ed25519 公钥堡垒机会「安静地接受」，
但 koko 一律拒绝 —— 症状是 `Permission denied (password,publickey)`，
看起来像凭据错，其实是密钥类型错。

```bash
# 1) 生成 RSA 私钥（如果还没有）
ssh-keygen -t rsa -b 4096 -f ~/.ssh/id_rsa

# 2) 拿到公钥内容
cat ~/.ssh/id_rsa.pub
```

注册方式有两条：

- **走 API**：用管理面工具 `jms_user_sshkey_add`（`confirm=true`）。
  它会写 `SSHKey` 表 —— 也就是真正生效的那张表 —— 并回读验证。
- **走堡垒机 UI**：个人设置 → SSH 公钥 → 粘贴。

⚠️ **不要往 `User.public_key` 字段写**。那个字段 PATCH 回来是 200、
`can_public_key_auth` 报 True，但**什么都没存**，密钥也登不上。

### 2.3 接线

两个服务都是 **stdio** 传输。执行面用 `mcp update` 指向运行时的二进制：

```bash
mcp update name=jumpserver-exec command=<jumpserver-mcp-exec 的绝对路径>
```

`env` 是只写字段（写进去读不回来），一次性把变量都给全：

```bash
mcp update name=jumpserver-exec \
  command=/usr/local/bin/jumpserver-mcp-exec \
  env='{"JMS_BASTION_HOST":"jumpserver.example.com","JMS_KOKO_USER":"ai","JMS_KOKO_KEY":"~/.ssh/id_rsa","JUMPSERVER_URL":"https://jumpserver.example.com","JUMPSERVER_TOKEN":"<token>"}'
```

或者直接编辑你的 MCP 客户端配置文件（下例以 JSON 形式为准；不同客户端的路径不同）：

⚠️ **`env` 里的 token 不要提交进 git 仓**：

```json
{
  "mcpServers": {
    "jumpserver-exec": {
      "type": "stdio",
      "command": "/usr/local/bin/jumpserver-mcp-exec",
      "env": {
        "JMS_BASTION_HOST": "jumpserver.example.com",
        "JMS_KOKO_USER": "ai",
        "JMS_KOKO_KEY": "~/.ssh/id_rsa",
        "JUMPSERVER_URL": "https://jumpserver.example.com",
        "JUMPSERVER_TOKEN": "<token>"
      }
    }
  }
}
```

### 2.4 自检（**接线完先跑这个**）

二进制有 `--doctor`，不依赖 MCP 会话，可以直接从 shell 跑：

```bash
jumpserver-mcp-exec --doctor    # 执行面接线 + 真实 koko 登录验证
```

`doctor` 同时也是子命令形态（`jumpserver-mcp-exec doctor`，与 `--doctor` 等价），
`--help` 打印用法。退出码沿用 OEKIT `pkg/cli` 的约定：`0` = 正常、`1` = 有问题、
`2` = 用法错误（未知子命令 / 参数不合法）。

⚠️ **`--help` / `-h` / `help` 的退出码是 `0`，不是 `2`** —— 问帮助不是错误路径。

---

## 3. 环境变量完整表

### 3.1 执行面自己的

| 变量 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `JMS_BASTION_HOST` | `tui` 后端必填 | **无** | koko 所在主机。⚠️ **没有默认值**：留空会让拨号目标变成 `:2222`，而空主机在 Go 里是**本机** ⇒ 私钥会送到本机 2222 上的东西。`auto` 后端下可不填（只影响终端兜底） |
| `JMS_KOKO_USER` | `tui` 后端必填 | **无** | 堡垒机登录用户（注册了 SSH 公钥的那个）。⚠️ 同样没有默认值：委派身份解析不到账号时会回落到 `ai` |
| `JMS_KOKO_KEY` | 否 | `~/.ssh/id_rsa` | 私钥路径。**必须是 RSA** |
| `JMS_KOKO_PORT` | 否 | `2222` | koko 的 SSH 端口 |
| `JMS_BACKEND` | 否 | `auto` | 会话怎么开：`token`（连接令牌）、`tui`（走一遍 koko 全屏终端）、`auto`（先令牌，令牌机制不可用时回退终端） |
| `JMS_POOL_TTL` | 否 | `2m` | 空闲连接在池里留多久。连接池让同一台机器的第二次调用几乎免费（实测 1.04s → 0.26s）。⚠️ 池里的连接**不出现在堡垒机的会话列表里**（堡垒机记的是每条命令的 exec 会话，跑完就结束）；但资产上会留一个 `sshd-session: <user> [priv]` 进程，所以这个值越长，资产上挂着的进程越多 |
| `JMS_BACKEND_MEMORY_TTL` | 否 | `10m` | **后端记忆**记多久：记住上次走通的是哪条路（令牌还是终端），下次先试它。记忆只改**顺序**，从不跳过一条路，也从不把资产问题变成兜底。⚠️ **会过期**：过期后回到默认顺序（先令牌）—— 令牌这条路恢复了只能靠**再试一次**找回来。设 `0` 即关闭记忆 |
| `JMS_READ_ONLY` | 否 | `false` | 只读模式：执行面拒掉 `jmsx_exec` / `jmsx_run` / `jmsx_exec_multi` / `jmsx_put_file` / `jmsx_bg_start`，只留采集与清单 |

### 3.2 与管理面共用

| 变量 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `JUMPSERVER_URL` | 否 | **无** | 堡垒机地址，不要带尾部斜杠（代码会 `TrimRight`），也不带 `/api/v1`。⚠️ **执行面不强制**：缺了连接令牌那条路就没有（只走 koko 终端），资产清单工具也停用 |
| `JUMPSERVER_TOKEN` | 否 | 无 | JumpServer **PrivateToken**，呈现为 `Authorization: Token <key>`。⚠️ **不是 v5 专有**：v4 也注册了 `PrivateTokenAuthentication`。缺失时**执行面不启动失败**（它可以只走 koko），但资产清单与令牌后端都停用 |
| `JUMPSERVER_USERNAME` | 否 | 无 | 另一种凭据：**用账号密码登录**，堡垒机换成 Bearer token（`POST /api/v1/authentication/auth/`） |
| `JUMPSERVER_PASSWORD` | 跟 `JUMPSERVER_USERNAME` 配 | 无 | 上面那个登录的密码。⚠️ **优先读文件**（`~/.config/jumpserver-mcp/password`，0600），环境变量是退路 —— 长跑进程的环境在 Linux 上是 `/proc` 里谁都能读的 |
| `JUMPSERVER_OTP` | 否 | 无 | base32 的 TOTP 密钥。**只在堡垒机要求二次验证时用得上** |
| `JUMPSERVER_VERSION` | 否 | `auto` | 堡垒机是 **v4** 还是 **v5**：`v4` / `v5` / `auto`（探测）。⚠️ 它只决定**一件事**：v4 **没有** Chat AI 委派方案 ⇒ 会话只能以 API 凭据的身份跑（= 管理员面） |
| `JMS_REQUESTS_PER_SECOND` | 否 | `20` | 客户端对堡垒机的请求速率上限（令牌桶） |
| `JMS_BURST` | 否 | `40` | 令牌桶的突发额度 |
| `JUMPSERVER_TIMEOUT` | 否 | `90s` | 单次 API 调用超时 |
| `JMS_SERVER_NAME` | 否 | `jumpserver-mcp-exec` | 服务在门禁审计留痕里的标识（留痕的 `service` 字段） |
| `JMS_AUDIT` | 否 | `file` | 门禁留痕要不要**落盘**：`file`（每个进程一份 JSONL）或 `off`（只写 stderr） |
| `JMS_AUDIT_DIR` | 否 | `~/.local/state/jumpserver-mcp/audit` | 留痕文件放哪（目录 0700、文件 0600）。两个服务写同一个目录 |
| `JMS_AUDIT_RETENTION_DAYS` | 否 | `7` | 留痕文件保留多少天。**0 或负数 = 不按时间清**（⚠️ 但**空文件**那条规则照样删：安静超过一小时的空文件与保留期无关）。⚠️ 保留期是**文件级**的：超过保留期**且进程已结束**的文件会被清理；正在运行的进程的文件不删 ⇒ 文件里的行可能比这个窗口更老 |

⚠️ **两种凭据都配了怎么办**：`JUMPSERVER_TOKEN` 赢 —— 它不过期，登录换来的 Bearer token 会过期。

### 3.3 可选：Chat AI 委派凭据

| 变量 | 说明 |
|---|---|
| `JMS_DELEGATION_SECRET` | 堡垒机的 `CHAT_AI_DELEGATION_SECRET`（64 字节）。**别写进 MCP 配置** —— 那等于让能读该目录的每个进程都拿到它 |
| `JMS_DELEGATION_USER_ID` | 委派凭据以哪个用户身份执行。**执行面**不设则自动查 `JMS_KOKO_USER`（默认 `ai`）；**管理面**不设则自动查 `username=ai` |
| `JMS_DELEGATION_ORG_ID` | 组织 id。不设则自动查 `DEFAULT` |

secret 的**推荐存放位置**（权限 0600）：

```bash
mkdir -p ~/.config/jumpserver-mcp
chmod 700 ~/.config/jumpserver-mcp
printf '%s' "$CHAT_AI_DELEGATION_SECRET" > ~/.config/jumpserver-mcp/delegation.key
chmod 600 ~/.config/jumpserver-mcp/delegation.key
```

程序会依次找 `JMS_DELEGATION_SECRET` 环境变量 →
`~/.config/jumpserver-mcp/delegation.key` →
`~/.local/etc/jumpserver-mcp/delegation.key`。

**委派凭据是什么**：一次性、单次、60 秒内有效、绑定
「方法 + 路径 + query + body」的凭据。它**不额外授权** —— 走的是正常 RBAC，
只是让「不想长期可达」的操作不留长期凭证。

⚠️ **关键性质**：委派请求在堡垒机侧的身份是
`JMS_DELEGATION_USER_ID` 指向的用户（例如 `ai`），**不是** token 的所有者。

### 3.4 两条会话路径

执行面开一个会话有两条路，代价差着六七倍：

| | `token`（连接令牌） | `tui`（koko 全屏终端） |
|---|---|---|
| 开会话 | 实测 **0.7–0.9 秒** | 实测 **7.25–9.12 秒** |
| 单条命令 | 实测 0.15–0.25 秒 | 实测约 1.5 秒 |
| 退出码 | **原生** | 自己解析屏幕上的标记 |
| 大输出 | 完整（实测 134KB / 4000 行无截断） | 受 40 行窗格限制，要 `full=true` 再拉一次 |
| 文件传输 | 一次往返读回整段 | base64 分块塞进窗格 |

`auto`（默认）先试令牌。**只有令牌机制本身不可用时**（端点不存在、部署不提供、API 客户端没配）
才回退终端 —— 资产本身的问题（没有账号、没权限）不回退，因为终端也会以同样的理由失败，只是慢十秒。

**后端记忆**：如果这个堡垒机压根不提供令牌机制（或者 API 挂着），每一次调用都要先白付一次
失败的尝试 —— 一次扫 50 台就是白付 50 次。所以服务端**记住上次走通的是哪条路**（按堡垒机一条，
不是按资产：令牌是每次现签的，koko 拒令牌登录就是拒所有），下次先试它。

- 记忆**只改顺序**，从不跳过一条路，也从不把资产问题变成兜底；
- 记忆**会过期**（`JMS_BACKEND_MEMORY_TTL`，默认 10m）：过期后回到默认顺序（先令牌）——
  令牌这条路恢复了只能靠**再试一次**找回来，记忆不掩盖恢复
  （⚠️ 所以成功**不会**给记忆续期：否则它永远不过期，另一条路的恢复就永远被盖住）；
- TUI 优先时**先做资产检查**：走一遍终端要十秒，而没账号的资产终端也拒。
  这一查是**实时**的（不走 5 分钟缓存）—— 拿缓存做硬拒绝会把刚授权的机器拒掉。

**自愈**：会话开着的时候连接断了、重连又失败，而失败是**机制**问题 ⇒ 换终端跑这条命令。
⚠️ 这只在「命令**没有投递**」时发生 —— 换路执行是**第一次投递**，不是重放
（已投递的命令绝不重放那条规矩仍然成立）。降级之后答复里的 `via` 写**实际走的那条路**，
而**账号会变成未知**（终端路径由 koko 选账号，我们不选 —— 宁可说不知道，
也不报一个可能不是它的账号）。

令牌是**单次**的：签发一次只能用一次连接，连上以后一条连接里可以开多条 exec 通道。

⚠️ **身份**：令牌默认通过**委派凭据**签发，令牌的 `user` 就是被委派的那个用户（例如 `ai`），
所以会话的权限面和以前一模一样。没有配到委派 secret 时，令牌会用 API token 签 ——
那是一个**管理员**的权限面，比 `ai` 宽。`jumpserver-mcp-exec --doctor` 的 `identity` 一行
会写明当前是哪种。

### 3.5 连接池

连接池让**同一台机器的第二次调用几乎免费**：连接认证一次（令牌只签一次），
之后开多少条 exec 通道都行。

- 键是**解析后的资产 UUID**，不是调用方写的字符串 —— 所以 `node-071` 和
  `192.0.2.71`（同一台机器）共用一条连接，而不是两条。
- 闲置的连接会在 `JMS_POOL_TTL` 之后被回收。
- ⚠️ 池里的连接**不出现在堡垒机的会话列表里**（堡垒机记的是每条命令的 exec
  会话，跑完就结束）；但资产上会留一个 `sshd-session: <user> [priv]` 进程。
- `jmsx_pool_status` 可以查池里现在有什么、每条闲置多久。
- ⚠️ **安全读法**：池里的连接带着它被签发时的权限。**权限被撤销后，已经开着的
  连接仍然能用** —— 所以 TTL 也是「被撤销的权限多久才真正失效」。要更快生效就调小它。

### 3.6 传输与中转

- **上传 / 下载走 exec 通道的 stdin/stdout**：文件字节直接走通道，
  没有 base64、没有分块、没有命令行长度上限。实测 **1MB 上传 1.1s、下载 1.0s**
  （base64 分块要 25s）。
- **通道传输需要连接令牌后端**：终端路径是往屏幕里打字，载荷得能活过一行文本，
  所以它只能走 base64 分块。
- **`jmsx_relay`** 把一台机器的文件直接传到另一台：字节从源端通道进目标端通道，
  **不落本地磁盘**。两端各算一次 sha256，答复里给出两个校验和。
  源和目标是同一台机器会被拒（写会截断正在读的文件）。
- 写远端文件是**原子**的（先写同目录临时文件再改名）⇒ 传输中途失败不会毁掉原文件。

### 3.7 执行面怎么拿资产和账号的 id

令牌要带**资产 UUID** 和**账号 UUID**，不是名字。所以执行面要先调 API 把名字解析成 id：

- `account` 必须是**账号 UUID**。填账号名或 alias 会被拒（`perm_account_invalid`）。
- 资产清单在执行面**缓存 5 分钟**：多机扫描每台机器都要解析一次资产，没有缓存就会
  「每台机器拉一次全量资产清单」，扫描时间全花在列清单上。

### 3.8 门禁留痕（落盘的那一份）

执行面每次工具调用都留一行痕：调了哪个工具、参数是什么（已脱敏）、门禁每一段的判定与理由、
放行与否、耗时、出参字节数、**投递状态**。**这份留痕要落盘**，原因是它记的是
「在这里**尝试过**什么」—— 而**被门禁拦下的命令根本没到堡垒机**，所以堡垒机那边没有任何会话、
没有任何日志。「谁试图在那台机器上跑破坏性命令、被拦下了」这个问题，只有本机的留痕答得了。

| | |
|---|---|
| 落在哪 | `JMS_AUDIT_DIR` 下的 `audit-<日期>-<pid>-<随机>.jsonl`，**每个进程一份**（两个服务同时跑不会互相踩） |
| 权限 | 目录 0700、文件 0600（里面是每次调用的参数） |
| 格式 | 一行一个 JSON：`ts` / `service` / `tool` / `client` / `args` / `allowed` / `refused` / `dry_run` / `would_execute` / `failed` / `stages[]` / `duration_ms` / `out_bytes` / `error` / `delivery` |
| `service` | **服务名**：本服务是 `jumpserver-mcp-exec`。⚠️ **老留痕里写的是旧的服务名** ⇒ 按服务过滤时老记录**默认匹配不上** |
| `client` | **谁调的**：MCP 客户端在 initialize 时自报的名字。⚠️ 它是**客户端自报**的，不是认证过的身份 —— 当线索用可以，当证据用不行 |
| `allowed` 的口径 | **「没有被拒绝」，不是「跑成功了」**。`false` ⇒ 什么都没跑；`true` ⇒ 没被拒 —— 但它**可能失败了**，看 `failed` / `error` 那一列 |
| `failed` | **这次调用的「活」干砸了**。⚠️ **门禁拒掉一次调用不算失败** —— 那是门禁在干活 |
| `delivery` | **这次动作有没有真的送到机器上**：`not_sent` = 根本没离开这个进程（重试是安全的）；`maybe_sent` = **可能已经在机器上跑了**（**永不重放**）。⚠️ 缺这一格 = **没人说**，不是「肯定没送到」<br>⚠️ 多机的那几个工具（`jmsx_exec_multi` / `jmsx_collect_multi`）：**调用级**那一格是「任一主机可能还在跑就是 `maybe_sent`」；**逐台的**那一格在**答复的每一行**里 |
| `error` | **这次调用为什么以错误收场**。两种都装：**门禁拒的**与**工具失败的**。⚠️ 想知道「这里什么坏了」用 `failed=true` 筛 |
| 脱敏 | **每一列自由文本都过同一套脱敏**：参数、错误、**拒绝理由**、门禁每一段的理由 |
| `would_execute` | **只在知道时才有这一格**：这次演练若真跑，会不会跑。⚠️ 「答复里没这一格」与「这一格是 `false`」是**两件事** |
| `refused` 与 `dry_run` | **演练不是「被拦下」**。查「谁被拦下了」用 `refused=true`，别用 `allowed=false` |
| `stages[]` 为空 | **这次调用在到达门禁之前就被挡下了** —— SDK 的输入契约校验先拒了它 |
| 载荷字段 | `content` / `content_base64` / `body` 这类**载荷**只记长度不记内容 |
| `args` 截断 | 超过 500 字节会截断，**切在字符边界上** |
| 文件大小 | 单个文件超过 32MB 自动轮转（归档成 `.1`，**rename 是原子替换**）。⚠️ **轮转失败时文件留痕就此停止** —— **单文件**的上限是磁盘的**硬边界**；那一行日志会说明这件事，`--doctor` 的 `audit_trail` 也会报出来 |
| 怎么读 | `tail -f ~/.local/state/jumpserver-mcp/audit/*.jsonl*`（末尾那个 `*` 把轮转的 `.1` 也带上），或用 `jmsx_audit_query` 工具 |
| 一次最多几行 | `jmsx_audit_query` 单次**最多 500 行** —— 答复是一个列表，没有上限的列表一次就能塞满调用方的上下文 |
| 留痕目录为空时 | 写侧与读侧说**同一句话**：没有目录 = **没有留痕文件** |
| 关掉 | `JMS_AUDIT=off` —— 只留 stderr 那一行 |

⚠️ **数组里的裸字符串不被脱敏**：脱敏靠**键名** ⇒ 元素没有键名时原文进留痕。
⇒ **别把凭据放进没有键名的位置**。

---

## 4. 自检

### 4.1 `jumpserver-mcp-exec --doctor`

```
jumpserver-mcp-exec doctor
  bastion: jumpserver.example.com:2222 as ai
  key:     ~/.ssh/id_rsa
  backend: auto

  [OK  ] key_file        present, N bytes
  [OK  ] key_type        ssh-rsa, which is the only type koko accepts
  [OK  ] bastion         jumpserver.example.com:2222 as ai
  [OK  ] koko_port       jumpserver.example.com:2222 accepted a connection
  [OK  ] rest_client     the bastion API answered
  [OK  ] version         v5 (detected), delegation scheme: present
  [OK  ] identity        connection tokens are minted as the delegated user "ai" (the account the sessions have always run as)
  [OK  ] connection_pool 0 connection(s) open; an idle one is closed after 2m0s. ...
  [OK  ] audit_trail     ~/.local/state/jumpserver-mcp/audit (one JSONL file per process; read it with the jmsx_audit_query tool): N file(s), the oldest from <date>
  [OK  ] backend_memory  nothing remembered yet; the connection-token path is tried first
  [OK  ] koko_login      logged in on <asset> via the connection token backend as ai (opening took 0.83s, exit 0, host says "user\n<asset>")
result: healthy
```

⚠️ **`bastion` 那一行是配置行，不是网络行**：它说「堡垒机地址与登录账号配没配」。
host 或 user 没配时它的严重度分三档 —— 令牌后端不需要它们（OK）、`auto` 只丢终端兜底（WARN）、
两条路都不通（FAIL）。⚠️ **host 没配时 `koko_port` 不会被探测**：空地址在 Go 里是
**本机**，探它等于回答「这台机器的 2222 有没有东西在听」，然后把它当成堡垒机的可达性报出来。

`key_type` 这一项最值钱：它判定私钥的真实类型，能提前抓住「ed25519 被堡垒机
安静接受、koko 一律拒绝」这个坑。判定走 `oekit/network/sshx` 的
`DetectKeyType`，三级回落：**直接解析私钥**（最准，且不需要外部进程）→
`ssh-keygen -y`（带口令的私钥解析不了，但有 agent 时它能读）→ 文件头嗅探
（最后手段，只给粗粒度结论）。

`koko_login` 是**真实登录一次**（会建立录像会话），所以最慢也最有说服力。

⚠️ **`version` 与 `identity` 这两行在 v4 上会换一副面孔**：`version` 报 `v4`，
`identity` 变成 WARN，说清「这个部署没有 Chat AI 委派方案 ⇒ 连接令牌用 API 凭据签
⇒ 会话带的是那个凭据的权限（管理员面），而不是 `ai` 那个更窄的面」。
⚠️ 这**不是**配置问题 —— v4 上没有更窄的凭据可用。

---

## 5. 工具选择速查

### 5.1 在机器上执行

```
要在这台机器上执行命令
├── 一条命令                  → jmsx_exec
├── 多条命令 / 有依赖关系     → jmsx_run      （一次会话，实测省 ~40%）
├── 采集多个事实（要带标签）   → jmsx_collect  （一次会话，返回命名好的 map）
├── 多台机器同一条命令        → jmsx_exec_multi （带每台的输出！）
├── 多台机器采同一组事实      → jmsx_collect_multi （矩阵：行=机器，列=事实）
├── 超过 10 分钟的长任务      → jmsx_bg_start + jmsx_bg_check
├── 输出超过 40 行            → jmsx_exec 加 full=true
└── 传文件
    ├── 本地已有的文件        → jmsx_put_file（local_path）
    ├── 内容在手上            → jmsx_put_file（content / content_base64）
    └── 取远端文件的一段      → jmsx_get_file（offset / length）
```

`jmsx_exec_multi` **默认并发 4 台**（`concurrency` 可调，1~8）。每台机器一条独立
录像会话，所以并发不会串数据。实测 6 台机器：

| 并发 | 耗时 |
|---|---|
| 1（串行） | 54.9s |
| 4 | **25.5s** |

（串行时每台都要单独付一次约 10 秒的 koko TUI 握手；并发让这些握手重叠。）

`jmsx_bg_start` 把长任务**脱离会话**跑（`setsid`），退出码写在 `.rc` 标记文件里 ——
**靠它区分「还在跑」和「跑完了」**，光看输出文件存在是判断不出来的。

### 5.2 别的

| 要做什么 | 工具 |
|---|---|
| 看这台机器通不通 | `jmsx_reachable` |
| 列资产（要 REST 凭据） | `jmsx_inventory` |
| 看这条命令会不会被判危险 | `dry_run=true`（命令类工具都有） |
| 看一次会话花了多久 | `jmsx_session_cost` |
| 看连接池里有什么 | `jmsx_pool_status` |
| 查「谁在这里尝试过什么」 | `jmsx_audit_query` |

---

## 6. 故障排查

| 现象 | 先查 | 原因 |
|---|---|---|
| 工具在会话里调不到 | 重启会话 | runtime 的工具目录是**会话开始时缓存**的 |
| `Permission denied (password,publickey)` | `--doctor` 的 `key_type` | ed25519 密钥，koko 只认 RSA |
| 登录报「没有可用的授权账号或终端协议」 | 管理面的 `jms_doctor` | 资产没 protocols / 没账号 / 授权的 accounts 是空列表 |
| 每次会话都慢十倍 | `--doctor` 的 `version` | 委派在 v4 上回 401 ⇒ 被读成「令牌机制不可用」 |
| `jmsx_exec` 返回空 | 看 `exit_code` 和 `truncated` | `exit_code=-1` = 未知（命令杀了 shell），改用 `sh -c '...'` 包一层；`truncated=true` = 开头被截掉了 |
| `jmsx_inventory` 不见了 | 有没有 `JUMPSERVER_TOKEN` | 没 REST 凭据时**它不注册** ⇒ 工具数是 14 不是 15 |
| 输出少了 40 行 | 走的是哪条路 | **终端兜底路径**的窗格只有约 40 行；令牌路径是完整的 |
| MCP 进程 exit 137 / 无 stderr | 换一个**新路径** | 受执行策略限制的环境里，覆盖已存在路径的二进制会被杀 |
| `replacement directory ... does not exist` | `./script/restore-go-modcache.sh` | Go module cache 被清空 |
| `go mod tidy` 失败 | 别 tidy | 受限网络下抓不到测试专用依赖，手改 `go.mod` |

---

## 7. 改代码 → 生效

```
改代码 → ./script/build.sh → python3 script/publish-mcp.py
       → mcp update（用 publish 输出的命令）→ 重启会话
```

| 目录 | 是什么 | 谁改 |
|---|---|---|
| 仓库源码目录 | **源码**，唯一真相 | 你 / AI |
| `./bin/` | **构建产物** | `script/build.sh` |
| `~/.local/jumpserver-mcp-exec-runtime/<stamp>/` | **运行时二进制**，MCP 配置指向这里 | `script/publish-mcp.py` |
| 你的 MCP 客户端配置文件 | **接线**，记录上面那个路径 + 环境变量 | `mcp update` |

**改源码 ≠ 生效。** 已经跑起来的 MCP 进程还持有旧二进制，必须重启会话。

> ⚠️ 发布必须进**新的时间戳目录**。在某些受执行策略限制的环境里，原地覆盖二进制会被杀
> （exit 137、无 stderr）。`publish-mcp.py` 已经这么做了，不要自己写 `cp` 覆盖。

改完记得看工具数：执行面加一个工具应该 **N+1**（15 → 16）。没变说明注册表漏了。
⚠️ **没有 REST 凭据时那个数是 14**（`jmsx_inventory` 不注册）。

> 开发环境的完整流程、验证要求与常见坑见 [`DEVELOPMENT.md`](DEVELOPMENT.md)。

---

## 8. 安全须知

- **token 不要进 git**。MCP 客户端的配置文件在仓外，正常不会被提交，但别手滑拷进仓里。
- **委派 secret 不要写进 MCP 配置**（见 §3.3）。
- **私钥的 passphrase 走 `ssh-agent`**，别塞进环境变量。
- **一切写操作都要 `confirm=true`**，包括传文件。
- **命令以资产账号身份执行，不提权**。要 root 自己写 `sudo -n`。
- ⚠️ **失败之后不要重试一条 `maybe_sent` 的命令** —— 它可能还在那台机器上跑着。

---

## 9. 给 AI agent 的操作手册

`skills/` 目录里是 agent 选工具和避坑的依据：

| skill | 内容 |
|---|---|
| `jumpserver-ops` | 入口：两个服务分工、工具速查、铁律、koko 协议、v5 坑表 |
| `jms-exec-ops` | 执行面：命令、并发、长任务、传文件、大输出 |

**改了工具要同步改 `skills/`** —— 那里有文档/代码脱节检查。
