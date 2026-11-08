package handler

import (
	"fmt"
	"strings"

	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/render"
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
	}
	// The verdict vocabulary (risk / dry_run / would_execute / blocked /
	// note) is filled by the shared writer, so the two planes cannot
	// drift in what a rehearsal says.
	render.FillRehearsal(res, riskClass, would, blocked, render.RehearsalNote)
	return res
}

// dryRunPutDir is the dry-run answer of a directory upload: the prescan's
// planning numbers, with no session behind them (issue #20).
//
// The prescan is LOCAL — lstat only, no content read, no bastion — so the
// numbers cost nothing and cannot lie about the transfer (they are the same
// walk the stream would make, branch for branch). would_execute is
// unconditionally true: nothing refused this rehearsal, and a finished
// prescan is the green light the caller asked for. The note carries what a
// planner reads next: shard a too-large tree, clean the skipped entries,
// fix a wrong path — all before any session exists.
func dryRunPutDir(asset, localPath, remotePath string) (*jmsexec.PutDirResult, error) {
	st, err := koko.PrescanDir(localPath)
	if err != nil {
		return nil, err
	}
	note := fmt.Sprintf("%s; local prescan of %s: %d files, %d bytes would stream to %s%s",
		render.RehearsalNote, localPath, st.Files, st.Bytes, remotePath,
		skippedHint(st.Skipped, st.SkippedNames))
	res := &jmsexec.PutDirResult{
		Asset:  jms.Opt(asset),
		Local:  jms.Opt(localPath),
		Remote: jms.Opt(remotePath),
		Files:  jms.OptInt32(int32(st.Files)),
		Bytes:  jms.OptInt64(st.Bytes),
		Note:   jms.Opt(note),
	}
	// The verdict vocabulary, same writer as the command tools' rehearsals:
	// risk is this tool's own catalogue class (the policy table classes
	// put_dir destructive), and would_execute=true — the rehearsal was not
	// refused, it answered.
	render.FillRehearsal(res, jms.Risk_destructive.String(), true, "", "")
	return res, nil
}

// skippedHint says what the prescan left behind, when it left anything —
// the same fact the real transfer's answer would carry, so the rehearsal is
// a rehearsal of THIS upload and not of an idealised one.
func skippedHint(skipped int, names []string) string {
	if skipped == 0 {
		return ""
	}
	plural := "ies"
	if skipped == 1 {
		plural = "y"
	}
	return fmt.Sprintf("; %d entr%s would be skipped (sockets/devices cannot cross a tar stream): %s",
		skipped, plural, strings.Join(names, ", "))
}
