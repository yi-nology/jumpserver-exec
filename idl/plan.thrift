/*
 * plan —— 一次批量作业要花多久（演练时给出的那份估算）。
 */
namespace go jmsexec

/**
 * 一次批量作业的成本估算。
 *
 * 这不是承诺，是**估算**：walk 与 command 的耗时是在这台堡垒机上实测的
 * （没有实测过就用文档默认值，并在 walk_source / command_source 里说清），
 * 而你自己的命令到底跑多久，只有跑完才知道 —— 所以 min_seconds 是地板，
 * max_seconds 是天花板，estimate_seconds 是拿来规划的数。
 */
struct SweepPlan {
  /** 几台机器。 */
  1: optional i32 hosts,
  /** 会开几个会话（每台机器一个）。 */
  2: optional i32 sessions,
  /** 每台机器几条命令。 */
  3: optional i32 commands_per_host,
  /** 实际并发（请求值会被夹到 [1, 8] 与机器数之间）。 */
  4: optional i32 concurrency,
  /** 分几轮跑完。 */
  5: optional i32 rounds,
  /** 每台机器大约多久。 */
  6: optional double seconds_per_host,
  /** 大约多久跑完。 */
  7: optional double estimate_seconds,
  /** 最快多久（只算走 TUI）。 */
  8: optional double min_seconds,
  /** 最慢多久（每条命令都跑满超时）。 */
  9: optional double max_seconds,
  /** estimate_seconds 的人话版。 */
  10: optional string estimate,
  /** 走一次 TUI 多久。 */
  11: optional double walk_seconds,
  /** 一条命令多久。 */
  12: optional double command_seconds,
  /** 想看实测值就用这个工具。 */
  13: optional string measure_with,
  /** 这个估算是怎么算出来的（一句话）。 */
  14: optional string basis,
  /** 这个估算不知道什么。 */
  15: optional string caveat,
  /** walk 的来源：实测还是默认值。 */
  16: optional string walk_source,
  /** command 的来源：实测还是默认值。 */
  17: optional string command_source,
  /** 拉满并发能省多少（并发已经是满的时候没有这一项）。 */
  18: optional ConcurrencyGain at_max_concurrency,
  /**
   * 其中几台已经有打开的连接（命中连接池）。
   *
   * 这几台开一次会话几乎免费 —— 不用再签令牌、再握手。
   */
  19: optional i32 pooled_hosts,
  /** 命中连接池时开一次会话要多久。 */
  20: optional double pooled_walk_seconds,
  /** 池命中成本的来源：实测还是默认值。 */
  21: optional string pooled_walk_source,
}

/** 把并发拉满能省多少。 */
struct ConcurrencyGain {
  1: optional i32 concurrency,
  2: optional i32 rounds,
  3: optional double estimate_seconds,
  4: optional string estimate,
}
