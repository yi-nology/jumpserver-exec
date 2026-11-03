# 安全策略（jumpserver-mcp-exec）

## 报告漏洞

**别开公开 issue，也别发公开 PR。**

请用 GitHub 的 [Private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidelines/reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability)
（Security → Report a vulnerability），或直接联系维护者。

请一并给出：受影响版本、复现步骤、你观察到的行为、你认为的影响面。

## 这个项目碰什么敏感东西

它本身**不存**任何凭据，但它**经手**这些：

| | 说明 |
|---|---|
| **koko 私钥** | 执行面用它 SSH 进堡垒机的 koko 端口。会话以堡垒机账号的身份跑命令 |
| **堡垒机 API token** | 用来换连接令牌（以及资产清单） |
| **堡垒机账号与授权面** | 会话的身份就是那个账号的权限面 |
| **命令原文与输出** | 留痕文件里可能有命令、路径、主机名、偶尔的凭据 |
| **被投递的命令** | ⚠️ **投递状态（`not_sent` / `maybe_sent`）错了 ⇒ 一条可能还在跑的 `rm -rf` 被跑第二遍** |
| **会话录像** | 走堡垒机自己，不经过本项目 |

## 使用者的纪律

- ⚠️ **别把 token / 私钥贴进 issue、PR、聊天、日志**；
- ⚠️ **别把 `JMS_KOKO_KEY` 指向一把没有 passphrase 的私钥**，除非那台机器本身就是隔离的；
  passphrase 走 `ssh-agent`，别塞进环境变量（进程的环境能被同机的其它进程读到）；
- ⚠️ **koko 只接受 `ssh-rsa`**：ed25519 会被静默拒绝，而拒绝的样子长得像权限问题 ——
  先跑 `--doctor` 看 `key_type` 一项；
- ⚠️ **执行面给最小权限的堡垒机账号**：它只需要「能登进目标资产」的权限，
  不需要管理员 —— 有 Chat AI 委派方案（v5）时，优先用委派凭据，让会话的权限窄于 API token；
  ⚠️ v4 上没有委派方案 ⇒ 会话只能是 API token 的权限面（**这不是配置能解决的**）；
- ⚠️ **先跑 `--doctor` 再接生产**：它会报出密钥类型、端口、真实 koko 登录；
- ⚠️ **留痕目录有保留期**（默认 7 天，`JMS_AUDIT_RETENTION_DAYS`）⇒「昨天发生了什么」可能已被清理；
- ⚠️ **`JMS_READ_ONLY=true`** 让这个 server 只读 —— 给只需要查询的 agent 用这个模式；
- ⚠️ **命令以资产账号身份执行，不提权** —— 要 root 自己写 `sudo -n`；
- ⚠️ **写操作都要 `confirm=true`**（危险命令才要判定，`jmsx_put_file` / `jmsx_relay` 一律要）。

## 留痕（audit trail）本身也是数据

留痕文件（默认 `~/.local/state/jumpserver-mcp/audit`，一进程一文件，0600）里记着
「谁在这台机器上试图跑什么」。**它也是敏感数据**：把它当审计资料保管，
别整目录丢进共享盘或日志系统。

⚠️ **老留痕里的 `service` 字段是旧的服务名**（见 [`CHANGELOG.md`](CHANGELOG.md)）⇒
按服务过滤时**老记录默认匹配不上**。

## 依赖与供应链

本仓依赖 `github.com/yi-nology/jumpserver-mcp-kit`（共用底座，Apache-2.0）、
`oagentkit/*`、`oekit/*`（MIT）、`modelcontextprotocol/go-sdk`（MIT→Apache-2.0 过渡）、
`google/jsonschema-go`（MIT）。构建**不写 `replace`**（依赖按普通方式解析，`go.sum` 钉住内容）。

⚠️ **底座在 [kit 仓](https://github.com/yi-nology/jumpserver-mcp-kit)** ——
它的漏洞会同时落在两个服务上；报告时注明你看到的是哪个服务的行为。

发现依赖有问题 ⇒ 按上面的私密渠道报，别在公开 issue 里贴 exploit。
