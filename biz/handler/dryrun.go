package handler

import (
	"fmt"
	"strings"
	"time"

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

// approvalMemory is the slice of the policy package's Memory that the
// confirmation gates need.
//
// It is an interface, not *policy.Memory, so this package does not import
// internal/service/policy: that package's own tests register these
// handlers, and a handler→policy import edge would make an import cycle in
// the policy test binary. *policy.Memory satisfies it; nil means "no
// memory" and every gate degrades to the plain risk verdict.
type approvalMemory interface {
	Enabled() bool
	TTL() time.Duration
	Grant(asset, command string)
	Satisfied(asset, command string) (bool, time.Time)
}

// rehearsalM is the confirmation gate with the kael-style approval memory:
// confirm=true grants, a live remembered approval satisfies, everything else
// falls through to the shared risk verdict. The note it returns goes into
// the answer's note field, so the trail shows WHY a dangerous command ran.
//
// The memory key is the EXACT command on the EXACT asset: an approval is
// reused only for the identical call, never widened to "similar" commands —
// widening is how a remembered rm -rf /tmp/x becomes a licence for
// rm -rf /var. And the memory only ever RELAXES the confirmation gate,
// never the read-only mode: the registration functions pass nil in
// read-only mode (the gate's whitelist currently lets conditional write
// tools reach the handler there), so under JMS_READ_ONLY=1 this degrades
// to the plain risk verdict.
func rehearsalM(mem approvalMemory, asset, command string, class jms.CommandClass, confirm bool) (riskClass string, would bool, blocked string, note string) {
	riskClass = class.String()
	if !risk.RequiresConfirmation(class) {
		return riskClass, true, "", ""
	}
	if confirm {
		if mem != nil && mem.Enabled() {
			mem.Grant(asset, command)
		}
		if mem != nil && mem.Enabled() {
			note = fmt.Sprintf("dangerous command confirmed and remembered; this exact command on this asset needs no further confirmation for %s", mem.TTL())
		}
		return riskClass, true, "", note
	}
	if mem != nil && mem.Enabled() {
		if ok, at := mem.Satisfied(asset, command); ok {
			return riskClass, true, "", fmt.Sprintf("confirmed via remembered approval granted at %s (expires %s)",
				at.Format(time.RFC3339), at.Add(mem.TTL()).Format(time.RFC3339))
		}
	}
	_, blocked = risk.Rehearsal(class, confirm)
	return riskClass, false, blocked, ""
}

// dryRunCommand is the dry-run answer of a single command.
func dryRunCommand(mem approvalMemory, asset, command string, class jms.CommandClass, confirm bool) *jmsexec.CommandResult {
	// It would run if it needs no confirmation — or if the confirmation is
	// already there, given live or remembered.
	riskClass, would, blocked, note := rehearsalM(mem, asset, command, class, confirm)
	if note == "" {
		note = render.RehearsalNote
	}
	res := &jmsexec.CommandResult{
		Asset:    asset,
		Command:  command,
		ExitCode: -1,
		Via:      "jumpserver koko (bastion), recorded",
	}
	// The verdict vocabulary (risk / dry_run / would_execute / blocked /
	// note) is filled by the shared writer, so the two planes cannot
	// drift in what a rehearsal says.
	render.FillRehearsal(res, riskClass, would, blocked, note)
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
