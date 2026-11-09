# 技能（执行面）

给 AI agent 用的操作手册。**每个 skill 是一个目录，含一个 `SKILL.md`**，
靠 frontmatter 的 `description` 决定什么时候被加载。

```
skills/
├── README.md              ← 本文件：安装与维护
└── jms-exec-ops/             ← 在机器上跑命令、批量并发、长任务、传文件、大输出
    ├── SKILL.md
    └── references/
        ├── koko-tui.md        ← koko TUI 协议（2026-10-25 实测）
        ├── health-check.md    ← 健康巡检配方（检查集+阈值，2026-11-10 分类实测）
        └── connectivity.md    ← 连通性排障配方（探测梯子+失败分类，2026-11-10 分类实测）
```

## 这个 skill 干什么

| skill | 什么时候加载 | 写操作? |
|---|---|---|
| `jms-exec-ops` | 在机器上跑命令、批量扫、传文件、传大输出、跑长任务、健康巡检与连通性排障配方 | 远端是 |

⚠️ **管理面的 skill 在另一个仓**：入口 skill `jumpserver-ops`、资产上线 `jms-onboarding`、
权限核查 `jms-access-review` 在
[`yi-nology/jumpserver-mgmt`](https://github.com/yi-nology/jumpserver-mgmt) 仓里 ——
它们跟着管理面服务走，不跟着执行面走。

## 安装

技能要放在 agent 的 skills 目录才会被加载（路径随客户端而定，例如
`~/.minimax/agents/mavis/skills/`、`~/.claude/skills/`、`~/.codex/skills/`）：

```bash
cd /path/to/jumpserver-exec
s=jms-exec-ops
mkdir -p ~/.minimax/agents/mavis/skills/$s
cp skills/$s/SKILL.md ~/.minimax/agents/mavis/skills/$s/
```

**skill 放在仓里而不是只放在 agent 目录**，是因为：
- 跟着代码走版本化，改了工具表不用手动同步两份
- 新会话、新 agent 装上就有
- 评审时能看出「文档说的」和「代码做的」是否一致

## frontmatter 约定

```yaml
---
name: <目录同名>
description: |
  <英文描述：什么时候用、触发词、什么时候不要用>
descriptions:
  zh-Hans: "<中文描述>"
displayNames:
  zh-Hans: "<中文显示名>"
---
```

`description` 是**唯一**决定加载时机的字段，所以必须写清
**触发词**（执行命令 / 批量下发 / 传文件 / 部署脚本 …）和**边界**（什么时候不要用）。

## 维护

改完执行面的 MCP 工具后，**这个 skill 的工具表要一起改**。

这不靠自觉 —— 管理面仓的 `script/check-skill-drift.sh` 会扫描**工作集**
（两个仓的技能一起看，因为执行面的 skill 会提到管理面的工具）：

```bash
cd /path/to/jumpserver-mgmt && ./script/check-skill-drift.sh
```

它检查三个方向，其中两个算错误：

- **文档 → 代码（错误）**：skill 里提到的工具必须真实存在。
- **工具名必须写全（错误）**：`jmsx_exec / run` 这种简写，agent 抄过去就成了调一个
  从不存在的东西。
- **代码 → 文档（只报告）**：速查表是**选择指南**而不是穷举，只报覆盖率。
