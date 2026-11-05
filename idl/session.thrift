/*
 * session —— 会话的账与连接池：一次会话跑了什么、开了多久、值多少钱。
 */
namespace go jmsexec

/**
 * 一个 koko 会话的统计。
 *
 * 这些键是**固定的**，不是动态 map —— 以前它是一个 map，于是「开一次会话
 * 要多久」这个数字没有形状，只能让人猜。walk_seconds 正是调用方规划一次
 * 批量作业时要用到的那个数，它必须说得清。
 *
 * command_seconds 只有跑过命令才有（跑过才知道平均一条命令多久）。
 */
struct SessionStats {
  /** 会话连着的机器。 */
  1: optional string asset,
  /** 这个会话里跑了几条命令。 */
  2: optional i32 commands_run,
  /** 会话开着多久了。 */
  3: optional i32 open_seconds,
  /** 走一遍 koko TUI 用了多久（开这个会话最贵的一步）。 */
  4: optional double walk_seconds,
  /** 后面每条命令省下的走路时间（合计）。 */
  5: optional i32 approx_saved_sec,
  /** 一条命令平均多久。 */
  6: optional double command_seconds,
  /**
   * 这个会话是不是用了池里已有的连接。
   *
   * 是的话，开这个会话几乎免费 —— 没有再签令牌、再握手。
   */
  8: optional bool reused,
  /**
   * 会话是用哪个后端开的。
   *
   * 有两条路：走一遍 koko 的全屏终端（贵），或者用连接令牌直接开一条
   * exec 通道（便宜，而且退出码是原生的）。调用方要知道自己的会话
   * 走的是哪条 —— 两种会话的代价差着六到七倍。
   */
  7: optional string backend,
}

/**
 * 一次会话值多少钱（jmsx_session_cost）。
 *
 * walk_seconds 是那个数：走一遍 TUI 要多久。同一会话里后面每条命令几乎
 * 是白送的，所以一次调用问几件事比一次问一件便宜得多。
 */
struct SessionCostResult {
  1: optional string asset,
  /** 探测命令的输出。 */
  2: optional string probe,
  /** 这个会话的账。 */
  3: optional SessionStats stats,
  /** 走一遍 TUI 多久。 */
  4: optional double walk_seconds,
  /** 一句话说明这个数怎么用。 */
  5: optional string note,
}

/**
 * 连接池里的一条连接（jmsx_pool_status）。
 */
struct PoolEntry {
  /** 这条连接连的是哪台机器。 */
  1: optional string asset,
  /** 它以哪个账号跑。 */
  2: optional string account,
  /** 以谁的身份被堡垒机授权。 */
  3: optional string identity,
  /** 多久没用了（秒）。 */
  4: optional double idle_seconds,
}

/**
 * 连接池的状态（jmsx_pool_status）。
 *
 * 连接池让同一台机器的第二次调用几乎免费 —— 它不签令牌、不握手。
 * 这个工具回答的是「池里现在有什么」「为什么重复扫这么快」。
 */
struct PoolStatus {
  /** 现在开着几条连接。 */
  1: required i32 connections,
  /** 空闲的连接多久会被回收（秒）。 */
  2: required double idle_ttl_seconds,
  /** 走的是哪个后端。 */
  3: optional string backend,
  /** 池里每条连接。 */
  4: optional list<PoolEntry> entries,
  /** 一句话说明这个池意味着什么。 */
  5: optional string note,
}
