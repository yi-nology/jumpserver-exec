/*
 * run —— 在机器上跑命令：单条、连通性探测、一条会话一批、多机批量。
 */
namespace go jmsexec
include "plan.thrift"
include "session.thrift"

/**
 * 一条命令在受管机器上的结果。
 *
 * exit_code 为 -1 表示没有拿到退出码（命令多半把 shell 一起带走了，
 * 或者输出无法解析）—— 那不是「失败」，是「不知道」。
 */
struct CommandResult {
  /** 命令跑在哪台机器上。 */
  1: required string asset,
  /** 实际执行的命令。 */
  2: required string command,
  /** 退出码；-1 表示拿不到。 */
  3: required i32 exit_code,
  /** 合并后的输出（stdout 与 stderr）。 */
  4: required string output,
  /** 这条结果是怎么来的（经堡垒机 koko，有录像）。 */
  5: required string via,
  /** 输出是否被终端窗口截断了。 */
  6: optional bool truncated,
  /** 截断或退出码缺失时，调用方该怎么做的提示。 */
  7: optional string warning,
  /** 重新拉取完整输出后的字节数。 */
  8: optional i32 output_bytes,
  /** 重新拉取完整输出失败时的原因。 */
  9: optional string full_output_error,
  /**
   * 命令被判为危险（rm -rf、重启、提权……）而调用方没有确认时，这里写明
   * 为什么没执行、以及要怎么重来。出现这个字段就意味着命令**没有跑**。
   */
  10: optional string blocked,
  /**
   * 命令的分类（READ / MODIFY / DANGEROUS / UNKNOWN）。被拦下时出现 ——
   * 那正是调用方需要知道「它为什么被拦」的地方。
   */
  11: optional string risk,
  /** 这是不是一次演练（dry_run）：命令没有执行。 */
  12: optional bool dry_run,
  /**
   * 如果真的执行，会不会执行。
   *
   * 演练的意义就在这里：一条 DANGEROUS 命令在没有 confirm 时
   * would_execute=false，带着 confirm 时 would_execute=true —— 调用方
   * 先看这个，再决定要不要跑。
   */
  13: optional bool would_execute,
  /** 一句话说明这次答复是什么（演练、还是被拦下）。 */
  14: optional string note,
  /**
   * 这次会话是不是用了池里已有的连接。
   *
   * 是的话就没有再签令牌、再握手 —— 开这个会话几乎免费。
   */
  15: optional bool reused,
  /**
   * 命令是以哪个账号身份跑的（资产上的登录名）。
   *
   * 资产上可能挂着好几个账号，会话落在哪一个上是有后果的 ——
   * 所以答案里要写明，而不是让调用方去猜。
   */
  16: optional string account,
}

/**
 * 这台机器能不能连上。
 *
 * reachable=false 时 error 说清是哪一步不行；reachable=true 时
 * whoami_hostname 是那次探测的输出（连上以后跑的是 id -un; hostname）。
 */
struct ReachableResult {
  1: required string asset,
  2: required bool reachable,
  /** 探测命令的输出（id -un; hostname）。 */
  3: optional string whoami_hostname,
  /** 探测命令的退出码。 */
  4: optional i32 exit_code,
  /** 连不上时的原因。 */
  5: optional string error,
  /**
   * 这次动作**有没有真的送到机器上**。
   *
   *   not_sent    根本没离开这个进程（重试是安全的）
   *   maybe_sent  可能已经在机器上跑了（**永不重放**）
   *
   * ⚠️ 缺这一格 = **没人说**，不是「肯定没送到」。
   */
  6: optional string delivery,
}

/**
 * 一条会话里跑的几条命令（jmsx_run）。
 *
 * 同一个答复形状覆盖三种情形：跑完了（results 有内容）、被拦下了
 * （risk + blocked）、演练过（dry_run + would_execute）。演练与拦下时
 * results 不出现 —— 什么都没跑，没有结果可言。
 */
struct BatchResult {
  1: required string asset,
  /** 跑（或要跑）的命令，按顺序。 */
  2: optional list<string> commands,
  /** 每条命令的输出，按顺序，与 commands 一一对应。 */
  3: optional list<string> results,
  /** 这个会话的账。 */
  4: optional session.SessionStats stats,
  /** 风险分级。 */
  5: optional string risk,
  /** 为什么没跑。 */
  6: optional string blocked,
  /** 这是演练。 */
  7: optional bool dry_run,
  /** 真跑的话会不会执行。 */
  8: optional bool would_execute,
  /** 一句话说明这次答复是什么。 */
  9: optional string note,
  /** 跑了一半出的问题。 */
  10: optional string error,
  /** 是不是只跑了一半。 */
  11: optional bool partial,
  /**
   * 这次动作**有没有真的送到机器上**。
   *
   *   not_sent    根本没离开这个进程（重试是安全的）
   *   maybe_sent  可能已经在机器上跑了（**永不重放**）
   *
   * ⚠️ 缺这一格 = **没人说**，不是「肯定没送到」。
   */
  12: optional string delivery,
}

/** 批量执行里的一台机器。 */
struct HostResult {
  /** 机器。 */
  1: optional string asset,
  /** 这台跑成了没有。 */
  2: optional bool ok,
  /** 退出码。 */
  3: optional i32 exit_code,
  /** 输出。 */
  4: optional string output,
  /** 这台花了多久。 */
  5: optional double seconds,
  /** 输出被终端窗口截断了。 */
  6: optional bool truncated,
  /** 截断了该怎么做。 */
  7: optional string warning,
  /** 出错的原因。 */
  8: optional string error,
  /** 没轮到它（stop_on_error 生效时被跳过）。 */
  9: optional bool skipped,
  /**
   * 这个会话是不是用了池里已有的连接。
   *
   * 是的话就没有再签令牌、再握手 —— 这一台的秒数因此小得多。
   */
  10: optional bool reused,
  /**
   * **这一台**的命令有没有真的送到机器上。
   *
   *   not_sent    根本没离开这个进程（重试是安全的）
   *   maybe_sent  可能已经在机器上跑了（**永不重放**）
   *
   * ⚠️ 缺这一格 = **没人说**，不是「肯定没送到」。
   * ⚠️ 调用级的那一格在答复的顶层（任一主机可能还在跑就是 `maybe_sent`）；
   * **逐台的**就是这一格 —— 「哪台可能还在跑」不用去读文字。
   */
  11: optional string delivery,
}

/**
 * 一次批量执行的结果（jmsx_exec_multi）。
 *
 * results 按传入的机器顺序排列（跳过的也占位），所以调用方不必自己排序
 * 就能把结果和机器列表对上。
 */
struct ExecMultiResult {
  /** 扫了哪几台（按传入顺序）。 */
  1: optional list<string> assets,
  /** 跑的命令。 */
  2: optional string command,
  /** 实际处理了几台。 */
  3: optional i32 hosted,
  /** 跑成了几台。 */
  4: optional i32 ok,
  /** 失败几台。 */
  5: optional i32 failed,
  /** 跳过几台。 */
  6: optional i32 skipped,
  /** 实际并发。 */
  7: optional i32 concurrency,
  /** 整体花了多久。 */
  8: optional double seconds,
  /** 每台机器的结果。 */
  9: optional list<HostResult> results,
  /** 一句话说明这次答复。 */
  10: optional string note,
  /** 这次作业要花多久（演练时给出）。 */
  11: optional plan.SweepPlan plan,
  12: optional string risk,
  13: optional string blocked,
  14: optional bool dry_run,
  15: optional bool would_execute,
  /**
   * 这次动作**有没有真的送到机器上**。
   *
   *   not_sent    根本没离开这个进程（重试是安全的）
   *   maybe_sent  可能已经在机器上跑了（**永不重放**）
   *
   * ⚠️ 缺这一格 = **没人说**，不是「肯定没送到」。
   */
  16: optional string delivery,
  /**
   * 这次调用**有一部分没成**时的汇总说明。
   *
   * 多机的逐台结果在 hosts[] 里（每台自己说），但**调用级**也要说一句话
   * —— 否则留痕只看得见「有几台成了」，看不见「有的那台到底送到没有」。
   */
  17: optional string error,
}
