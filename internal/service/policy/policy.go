// Package policy holds the tool catalogue's risk side.
//
// For every tool of the execution plane: what it does to the target machine.
//
// The execution plane is the side that touches machines, so the distinction
// that matters here is "does this change the machine or only look at it":
//
//	jmsx_collect carries the CALLER's commands (the facts to collect), so it
//	is a command runner, not a reader — the same rule as jmsx_exec.
//	jmsx_exec runs the caller's command, whatever it is — it acts.
//
// The gate in front of the tools (jms-kit/pkg/reg) refuses every write in
// read-only mode, which is the switch to turn when an agent should look at
// the fleet but not touch it.
//
// Every tool that changes the machine asks for a confirmation, the same way
// the management plane's write tools do. The flag is required in the schema,
// so the model passes it without being asked twice, and the gate refuses the
// call without it.
package policy

import (
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
)

// Table is the tool catalogue's risk side.
var Table = reg.PolicyTable{
	// Looks.
	"jmsx_inventory": {Risk: jms.Risk_read},
	"jmsx_reachable": {Risk: jms.Risk_read},
	"jmsx_collect": {Risk: jms.Risk_write, Conditional: true,
		Hint: "confirm=true only when one of the commands is classified DANGEROUS"},
	"jmsx_collect_multi": {Risk: jms.Risk_write, Conditional: true,
		Hint: "confirm=true only when one of the commands is classified DANGEROUS"},
	"jmsx_get_file": {Risk: jms.Risk_read},
	"jmsx_get_dir":  {Risk: jms.Risk_read},
	// 状态检查本身是看，但 cleanup=true 在机器上删两个文件——能删东西的
	//「看」不是看（verifier 抓的洞：JMS_READ_ONLY=1 曾放行它，留痕记成
	// read）。写是**条件性**的（只有 cleanup 要）⇒ 普通轮询不受影响；
	// 只读模式整个停用本工具（能删东西的工具不进只读目录）。
	"jmsx_bg_check": {Risk: jms.Risk_write, Conditional: true,
		Hint: "confirm=true only with cleanup=true (removes the job's output and exit marker on the machine)"},
	"jmsx_session_cost": {Risk: jms.Risk_read},
	"jmsx_pool_status":  {Risk: jms.Risk_read},
	// The trail is this machine's own record of what was attempted here.
	// Reading it changes nothing on any machine, so it is a look like the
	// others.
	"jmsx_audit_query": {Risk: jms.Risk_read},

	// Acts.
	"jmsx_exec": {Risk: jms.Risk_write, Conditional: true,
		Hint: "confirm=true only when the command is classified DANGEROUS"},
	"jmsx_exec_multi": {Risk: jms.Risk_write, Conditional: true,
		Hint: "confirm=true only when the command is classified DANGEROUS"},
	"jmsx_run": {Risk: jms.Risk_write, Conditional: true,
		Hint: "confirm=true only when one of the commands is classified DANGEROUS"},
	"jmsx_bg_start": {Risk: jms.Risk_write, Conditional: true,
		Hint: "confirm=true only when the command is classified DANGEROUS"},
	"jmsx_put_file": {Risk: jms.Risk_destructive,
		Confirm: "set confirm=true to write a file on the machine"},
	// A directory upload overwrites same-name files on the machine, so it
	// is a write like any other — unconditional confirm, like put_file.
	"jmsx_put_dir": {Risk: jms.Risk_destructive,
		Confirm: "set confirm=true to write files on the machine"},
	// The relay writes a file on the DESTINATION machine, so it is a write
	// like any other. It does not run a command, so it has nothing to
	// classify.
	"jmsx_relay": {Risk: jms.Risk_destructive,
		Confirm: "set confirm=true to write a file on the destination machine"},
	// Same verdict as jmsx_relay: the destination machine is written to
	// (same-name files overwritten), so it rides the destructive gate.
	"jmsx_relay_dir": {Risk: jms.Risk_destructive,
		Confirm: "set confirm=true to write files on the destination machine"},
}
