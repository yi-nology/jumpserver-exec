/*
 * collect —— 事实的采集：要采什么、采回了什么（单机与多机矩阵）。
 */
namespace go jmsexec
include "plan.thrift"
include "session.thrift"

/** 一条要采集的事实：名字 + 产出它的命令。 */
struct Check {
  /** 名字（调用方自己起的标签）。 */
  1: optional string name,
  /** 产出这条事实的命令。 */
  2: optional string command,
}

/** 采集到的一条事实。 */
struct Fact {
  /** 产出它的命令。 */
  1: optional string command,
  /** 命令的输出。 */
  2: optional string output,
}

/**
 * 一台机器上的一批事实（jmsx_collect）。
 *
 * facts 是**按名字**索引的（不是按位置）—— 调用方给每条事实起了名字，
 * 用名字取回才不会把两台机器的输出对错行。
 */
struct CollectResult {
  1: required string asset,
  /** 要采的那些事实。 */
  2: optional list<Check> checks,
  /** 采回来的事实，按名字索引。 */
  3: optional map<string, Fact> facts,
  /** 这个会话的账。 */
  4: optional session.SessionStats stats,
  5: optional string risk,
  6: optional string blocked,
  7: optional bool dry_run,
  8: optional bool would_execute,
  9: optional string note,
  10: optional string error,
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

/** 批量采集里的一台机器。 */
struct MatrixRow {
  /** 机器。 */
  1: optional string asset,
  /** 这一行的事实。 */
  2: optional map<string, Fact> facts,
  /** 这台机器采成了没有。 */
  3: optional bool ok,
  /** 没采成的原因。 */
  4: optional string error,
  /** 这台机器花了多久。 */
  5: optional double seconds,
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
  6: optional string delivery,
}

/**
 * 一次批量采集的结果（jmsx_collect_multi）。
 *
 * matrix 是**一行一台机器**：每台机器一个自己的堡垒机会话，所以一台机器的
 * 事实不会跟另一台混在一起。矩阵的列就是 checks 里那些事实。
 */
struct CollectMultiResult {
  /** 扫了哪几台。 */
  1: optional list<string> assets,
  /** 采了哪些事实。 */
  2: optional list<Check> checks,
  /** 一行一台机器。 */
  3: optional map<string, MatrixRow> matrix,
  /** 采成了几台。 */
  4: optional i32 ok,
  /** 没采成几台。 */
  5: optional i32 failed,
  /** 实际并发。 */
  6: optional i32 concurrency,
  /** 整体花了多久。 */
  7: optional double seconds,
  /** 一句话说明这次答复。 */
  8: optional string note,
  /** 这次作业要花多久（演练时给出）。 */
  9: optional plan.SweepPlan plan,
  10: optional string risk,
  11: optional string blocked,
  12: optional bool dry_run,
  13: optional bool would_execute,
  /**
   * 这次动作**有没有真的送到机器上**。
   *
   *   not_sent    根本没离开这个进程（重试是安全的）
   *   maybe_sent  可能已经在机器上跑了（**永不重放**）
   *
   * ⚠️ 缺这一格 = **没人说**，不是「肯定没送到」。
   */
  14: optional string delivery,
  /**
   * 这次调用**有一部分没成**时的汇总说明。
   *
   * 逐台的结果在 matrix 里（每台自己说），但**调用级**也要说一句话 ——
   * 否则留痕只看得见「有几台成了」，看不见「有的那台到底送到没有」。
   */
  15: optional string error,
}
