package handler

import (
	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/risk"
)

// A dry run answers the question before the fact: what is this command
// classified as, and would it run?
//
// It costs no bastion session and has no side effect, so it is the cheap
// question to ask first — and the answer is the same one the confirmation
// gate would give, so a dry run is a rehearsal of the real call.

// rehearsal computes the three things every rehearsal and every refusal
// says the same way: what the command was classified as, whether it would
// run, and — when it would not — what the caller has to do about it.
//
// The judgement and its sentence come from risk.Rehearsal, the ONE place
// the "would this run?" question is answered for both planes: a tool that
// answered it differently from its neighbour would be worse than no answer
// at all, and the two planes used to spell the sentence each for itself.
// This wrapper only adds the class name, which the answer shapes carry
// alongside.
func rehearsal(class jms.CommandClass, confirm bool) (riskClass string, would bool, blocked string) {
	would, blocked = risk.Rehearsal(class, confirm)
	return class.String(), would, blocked
}

// dryRunNote says what a rehearsal answer is, in one sentence.
const dryRunNote = "dry run: nothing was executed"

// dryRunCommand is the dry-run answer of a single command.
func dryRunCommand(asset, command string, class jms.CommandClass, confirm bool) *jmsexec.CommandResult {
	// It would run if it needs no confirmation — or if the confirmation is
	// already there.
	riskClass, would, blocked := rehearsal(class, confirm)
	res := &jmsexec.CommandResult{
		Asset:    asset,
		Command:  command,
		ExitCode: -1,
		Via:      "jumpserver koko (bastion), recorded",
		Risk:     jms.Opt(riskClass),
		DryRun:   jms.OptBool(true),
		// Nothing ran, so there is no exit code to report.
		WouldExecute: jms.OptBool(would),
		Note:         jms.Opt(dryRunNote),
	}
	if blocked != "" {
		res.Blocked = jms.Opt(blocked)
	}
	return res
}
