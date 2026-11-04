package handler

import (
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-kit/model/jms"
)

// blockedCommand is the answer of a command that was NOT executed.
//
// The command was classified DANGEROUS and the caller did not confirm it, so
// nothing ran: no session was opened, nothing was typed into the machine.
// The answer says so, says why, and says how to go on.
//
// It is a plan that was not carried out rather than an error — the same shape
// the management plane's jms_job_run answers with. An agent reads it, adds
// confirm=true and repeats the call.
func blockedCommand(asset, command, class string) *jmsexec.CommandResult {
	return &jmsexec.CommandResult{
		Asset:   asset,
		Command: command,
		// -1 means "no exit code" — there was no command to have one.
		ExitCode: -1,
		Via:      "jumpserver koko (bastion), recorded",
		Risk:     jms.Opt(class),
		Blocked:  jms.Opt("classified " + class + "; re-call with confirm=true to execute"),
	}
}
