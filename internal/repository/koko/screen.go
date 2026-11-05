package koko

// The screen parser: turning a koko terminal capture into command output.
//
// Everything here is a PURE function of the captured bytes — no session,
// no receiver — because the parsing is the part that has to be replayable
// against a saved capture (see testdata/ and the glued-row regression):
// a parser that can only run inside a live session cannot be debugged
// from what went wrong on one.
//
// The capture is not a rendered screen. tmux repaints the whole pane with
// cursor-move escapes and packs several short rows onto one physical row,
// so the payload arrives interleaved with frame decoration and partial
// repaints. The functions below take that apart: rows are found by their
// `cat -n` number, reassembled by epoch, and read through the frame bars
// that sit between columns.

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A Session runs several commands on one shell, and tmux repaints earlier
// output as it scrolls. Fixed markers therefore cannot tell this command's rows
// from the previous command's: `cat -n` restarts numbering at 1 every time, so
// a repaint of the previous result is indistinguishable from the current one --
// four different commands came back as four copies of the first answer.
//
// Giving every command its own token makes the region unambiguous: only text
// carrying *this* invocation's token can be part of this command's output.
type marks struct {
	begin, end, rc string
}

func newMarks() marks {
	tok := fmt.Sprintf("%d_%d", time.Now().UnixNano()%1e9, rand.Int63n(1e9))
	return marks{
		begin: fmt.Sprintf(markBeginFmt, tok),
		end:   fmt.Sprintf(markEndFmt, tok),
		rc:    fmt.Sprintf(markRCFmt, tok),
	}
}

// maxScreen bounds the retained output. A single paint can be large and the
// buffer is scanned repeatedly while waiting.

// connectedAsset pulls the asset name out of the connection banner.
//
// koko announces the session it is opening as
// "ssh://<user>@<asset> 正在连接 <user>(<user>)@<address>", which is the only
// place the session says out loud WHICH asset it ended up on.
func connectedAsset(screen string) string {
	if i := strings.Index(screen, "ssh://"); i >= 0 {
		rest := screen[i+len("ssh://"):]
		// The frame bars count as separators too: koko paints the
		// banner inside a tmux frame, and the bars would otherwise end
		// up in the name the report prints.
		if j := strings.IndexAny(rest, " \t│┃║"); j >= 0 {
			rest = rest[:j]
		}
		if k := strings.Index(rest, "@"); k >= 0 {
			rest = rest[k+1:]
		}
		if rest != "" {
			return rest
		}
	}
	if i := strings.Index(screen, connecting); i >= 0 {
		rest := screen[i+len(connecting):]
		if j := strings.Index(rest, "@"); j >= 0 {
			rest = rest[j+1:]
		}
		if j := strings.IndexAny(rest, " \t│┃║"); j >= 0 {
			rest = rest[:j]
		}
		return rest
	}
	return ""
}

// selectAsset walks the TUI: open the search box, type the asset, confirm, and
// land on a shell prompt.
//
// The flow was re-derived against the live bastion on 2026-10-25, because the
// previous one no longer worked at all:
//
//	wait for the panel   -> the hint bar shows "enter:连接"
//	"/"                  -> the search box opens ("我的资产 · 搜索:")
//	type the keyword     -> the box shows "我的资产 · 搜索:<keyword>"
//	Enter                -> the search runs; the hint bar gains "x:清除搜索"
//	Enter                -> the matching asset is selected
//	Enter                -> the account is accepted and the shell opens
//
// Two details cost real time to find. The search box is opened with "/" —
// typing straight into the panel does nothing, because the panel rather than
// the box has focus until "/" is pressed. And a search that matches nothing
// shows "没有匹配的资产" while the list stays unfiltered, so the next Enter
// would connect to whatever row happened to be highlighted. That is why the
// search is verified before the selection is confirmed, and why the
// connection banner is checked against the requested asset: connecting to the
// wrong machine and reporting success is worse than failing.

// stripScaffolding drops the begin/rc marker lines and the echoed command line.
func stripScaffolding(s, command string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	skipEcho := false
	for i, l := range lines {
		if strings.Contains(l, "__JMSX_BEGIN_") {
			skipEcho = true
			continue
		}
		if skipEcho {
			// The shell echoes the whole command line right after the begin
			// marker; it is not program output.
			skipEcho = false
			if i == 0 || strings.Contains(l, command[:min(len(command), 24)]) {
				continue
			}
		}
		if strings.Contains(l, "__JMSX_RC_") {
			continue
		}
		if strings.Contains(l, "__JMSX_END_") {
			continue
		}
		out = append(out, strings.TrimRight(l, "\r"))
	}
	return strings.Join(out, "\n")
}

// resultSignature summarises how complete a capture looks: how many numbered
// rows it holds, plus whether the exit-code marker has arrived.
//
// Both halves are needed. Rows alone are not enough — a command whose only
// output IS the exit code (`false`, `sh -c 'exit 42'`) yields zero rows and
// would then never satisfy a "rows > 0" wait and hang until the deadline. That
// is exactly the regression that dropped exit-code passthrough.
// resultSignature is the "has the paint settled" signal: how many numbered
// rows there are, the exit code, and a fingerprint of their text.
//
// The text is the part that was missing. tmux paints a row's number and its
// text in the same repaint, but a wide payload can be caught mid-paint: the
// row is there with its number and no text yet. The row COUNT does not
// change while that happens, so a count-only signature reported "settled"
// and the parse handed back an empty line.

// resultSignature summarises how complete a capture looks: how many numbered
// rows it holds, plus whether the exit-code marker has arrived.
//
// Both halves are needed. Rows alone are not enough — a command whose only
// output IS the exit code (`false`, `sh -c 'exit 42'`) yields zero rows and
// would then never satisfy a "rows > 0" wait and hang until the deadline. That
// is exactly the regression that dropped exit-code passthrough.
// resultSignature is the "has the paint settled" signal: how many numbered
// rows there are, the exit code, and a fingerprint of their text.
//
// The text is the part that was missing. tmux paints a row's number and its
// text in the same repaint, but a wide payload can be caught mid-paint: the
// row is there with its number and no text yet. The row COUNT does not
// change while that happens, so a count-only signature reported "settled"
// and the parse handed back an empty line.
func resultSignature(s string, m marks, minEpoch int) (rows int, code int, fingerprint uint64) {
	r, c, _ := scanNumbered(s, m, minEpoch)
	h := fnv.New64a()
	for _, line := range r {
		_, _ = h.Write([]byte(line))
		_, _ = h.Write([]byte{0})
	}
	return len(r), c, h.Sum64()
}

// parseOutput reconstructs the command result from the captured screen.
//
// It deliberately does NOT assume the payload sits in one contiguous span
// between the markers. koko's tmux repaints the whole pane on every update, so
// the capture is hundreds of KB of interleaved frames: the begin marker, its own
// row, the result and the end marker routinely land in different repaints, with
// empty border rows in between. A 60-line result measured 967 KB and the very
// first 700 bytes after the begin marker were pure frame decoration.
//
// Instead every painted row is scanned, the ones carrying a `cat -n` number are
// collected, and the numbering reassembles them: line N appears once per repaint
// that included it, so the first occurrence wins and the rest are repaint noise.
// Output that genuinely repeats still gets distinct numbers, so nothing
// legitimate is dropped.
//
// scanNumbered below is the scanner that does that work and also recovers the
// command's exit code; parseOutput joins its lines.

// parseOutput reconstructs the command result from the captured screen.
//
// It deliberately does NOT assume the payload sits in one contiguous span
// between the markers. koko's tmux repaints the whole pane on every update, so
// the capture is hundreds of KB of interleaved frames: the begin marker, its own
// row, the result and the end marker routinely land in different repaints, with
// empty border rows in between. A 60-line result measured 967 KB and the very
// first 700 bytes after the begin marker were pure frame decoration.
//
// Instead every painted row is scanned, the ones carrying a `cat -n` number are
// collected, and the numbering reassembles them: line N appears once per repaint
// that included it, so the first occurrence wins and the rest are repaint noise.
// Output that genuinely repeats still gets distinct numbers, so nothing
// legitimate is dropped.
//
// scanNumbered below is the scanner that does that work and also recovers the
// command's exit code; parseOutput joins its lines.
func scanNumbered(s string, m marks, minEpoch int) ([]string, int, bool) {
	type row struct {
		n   int // session-unique sort key (epoch + line index)
		idx int // the plain cat -n line number, for gap detection
		t   string
	}
	var rows []row
	// seen maps a sort key to its position in rows: the same row can be
	// painted more than once, because tmux repaints the whole pane.
	seen := map[int]int{}
	code := -1

	for _, line := range strings.Split(s, "\n") {
		for _, col := range cleanFrameColumns(line) {
			// The candidates that are rows of THIS command. The ones that
			// are not — a piece of the output that happens to look like
			// a row, a row of an earlier command — are neither reported
			// nor allowed to END the text: a colour-highlighted "2024@foo"
			// inside the output is part of the output.
			//
			// Every candidate is tried, in order: trying only the first
			// would turn "a fragment took the row's place" into "the row
			// is gone" — the same wrong answer in the other direction.
			var ours []rowCandidate
			for _, cand := range numberedCandidates(col) {
				mt := numberedLine.FindStringSubmatch(col[cand.start:])
				if mt == nil {
					continue
				}
				if !rowIsOurs(mt, minEpoch) {
					continue
				}
				ours = append(ours, cand)
			}
			for ci, cand := range ours {
				start := cand.start
				loc := numberedLine.FindStringSubmatchIndex(col[start:])
				if loc == nil {
					continue
				}
				mt := numberedLine.FindStringSubmatch(col[start:])
				// The text ends where the NEXT row of this command begins,
				// or where the terminal moves the cursor to the next line —
				// so the content of another pane row cannot end up in this
				// row's answer.
				end := len(col)
				if ci+1 < len(ours) {
					end = ours[ci+1].start
				} else if c := cursorMoveIndex(col[start:]); c >= 0 {
					end = start + c
				}
				text := stripANSI(col[start+loc[6] : end])
				// The row is ours (rowIsOurs said so); the key is what
				// orders the rows and tells two paints of the same row
				// apart.
				key := mt[1]
				if mt[2] != "" {
					key = mt[1] + "-" + mt[2]
				}
				// Sorting numerically on the combined key keeps every command in
				// order while making the numbering unique across the session.
				n := 0
				if _, err := fmt.Sscanf(key, "%d-%d", &n, new(int)); err != nil {
					n = 0
					fmt.Sscanf(key, "%d", &n)
				}
				if mt[2] != "" {
					idx := 0
					fmt.Sscanf(mt[2], "%d", &idx)
					n = n*lineEpochStride + idx
				}
				if rc, ok := strings.CutPrefix(text, m.rc); ok {
					if code < 0 {
						if v, err := strconv.Atoi(strings.TrimSpace(rc)); err == nil {
							code = v
						}
					}
					continue
				}
				// The begin/end echoes are numbered like any other line, but they
				// are scaffolding, not output.
				if strings.Contains(text, m.begin) || strings.Contains(text, m.end) {
					continue
				}
				// The same row painted again: keep the MORE COMPLETE of the two.
				//
				// The first paint used to win outright, and that is wrong when
				// the first paint is incomplete: tmux can paint a row's number
				// and its text in different passes, so the first sighting of a
				// row may carry the number and no text at all. Taking that
				// empty paint made the command's output come back as "" — the
				// symptom being a command whose output was empty even though
				// it had plainly run.
				trimmed := strings.TrimLeft(text, " ")
				if pos, ok := seen[n]; ok {
					if len(trimmed) > len(rows[pos].t) {
						rows[pos].t = trimmed
					}
					continue
				}
				idx := 0
				fmt.Sscanf(mt[2], "%d", &idx)
				seen[n] = len(rows)
				rows = append(rows, row{n, idx, trimmed})
			}
		}
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].n < rows[j].n })
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.t)
	}

	// The pane is only as tall as the PTY, so a long result is cut off at the
	// top: the surviving rows keep their original cat -n numbers, leaving a gap
	// between 1 and the first number actually seen. A gap means output was lost,
	// and saying so matters more than the output itself -- an agent that does
	// not know is working from a silently incomplete answer.
	// Line 1 of the piped block is the begin marker and the second to last is
	// the rc marker, so real output starts at 2. When the pane is too short the
	// oldest rows scroll off the top and the first surviving number jumps well
	// past that -- that jump is the signal. (Comparing maxN against len(rows)
	// would false-positive on short output, because the two markers occupy
	// numbers without contributing rows.)
	//
	// The gap has to be read off the plain cat -n index, NOT off the sort key.
	// On a reused session the sort key is epoch*1000+idx, which is always far
	// above 3 — testing it made every result report truncated, turning the one
	// warning that matters into permanent background noise.
	truncated := false
	if len(rows) > 0 && rows[0].idx > 3 {
		truncated = true
	}
	return out, code, truncated
}

// parseOutput joins scanNumbered's rows into the command's output text.
// The why — repaint noise, numbered rows, gap detection — is told once, on
// scanNumbered, which is where that work happens.

// parseOutput joins scanNumbered's rows into the command's output text.
// The why — repaint noise, numbered rows, gap detection — is told once, on
// scanNumbered, which is where that work happens.
func parseOutput(s string, m marks, minEpoch int) (string, int, bool, error) {
	if !strings.Contains(s, m.end) {
		return "", -1, false, fmt.Errorf("no end marker in output")
	}
	if !strings.Contains(s, m.begin) {
		return "", -1, false, fmt.Errorf("no begin marker in output")
	}
	lines, code, truncated := scanNumbered(s, m, minEpoch)
	return strings.Join(lines, "\n"), code, truncated, nil
}

// frameSplit breaks a painted row into its columns. koko renders the asset's
// terminal inside a tmux pane, and tmux happily puts several short lines on one
// physical row, separated by frame bars:
//
//	││   1	hostname││   2	Linux││
//
// So a row is not a line of output; it is a set of columns, and the `cat -n`
// numbering injected by Exec is what puts them back in order.

// frameSplit breaks a painted row into its columns. koko renders the asset's
// terminal inside a tmux pane, and tmux happily puts several short lines on one
// physical row, separated by frame bars:
//
//	││   1	hostname││   2	Linux││
//
// So a row is not a line of output; it is a set of columns, and the `cat -n`
// numbering injected by Exec is what puts them back in order.
var frameSplit = regexp.MustCompile(`[│┃║]+`)

// rowCandidate is a place in a column where a numbered row may start.

// rowCandidate is a place in a column where a numbered row may start.
type rowCandidate struct {
	// start is where the row's prefix begins.
	start int
}

// cursorMoveIndex returns the index of the next cursor-positioning escape
// sequence in s, or -1.
//
// It marks the end of a row: tmux moves the cursor to the next pane row
// and writes there, so everything past it belongs to another row.

// cursorMoveIndex returns the index of the next cursor-positioning escape
// sequence in s, or -1.
//
// It marks the end of a row: tmux moves the cursor to the next pane row
// and writes there, so everything past it belongs to another row.
var cursorMoveRe = regexp.MustCompile(`\x1b\[[0-9;]*H`)

func cursorMoveIndex(s string) int {
	loc := cursorMoveRe.FindStringIndex(s)
	if loc == nil {
		return -1
	}
	return loc[0]
}

// rowIsOurs says whether a numbered row belongs to this command.
//
// Every row this tool writes carries the epoch of the command that wrote
// it, so a row WITHOUT an epoch is not one of ours: it can only be a piece
// of the output that happens to look like a row (a colour-highlighted
// "2024@foo", say), and letting it in would replace the real row with a
// fragment of the output — silently.
//
// A row with an OLDER epoch belongs to an earlier command: it is scrollback
// on the pane, not this answer.
//
// The plain form is still accepted when the caller does not know the epoch
// (minEpoch == 0), which is the case in the tests that pin the shape of a
// row.

// rowIsOurs says whether a numbered row belongs to this command.
//
// Every row this tool writes carries the epoch of the command that wrote
// it, so a row WITHOUT an epoch is not one of ours: it can only be a piece
// of the output that happens to look like a row (a colour-highlighted
// "2024@foo", say), and letting it in would replace the real row with a
// fragment of the output — silently.
//
// A row with an OLDER epoch belongs to an earlier command: it is scrollback
// on the pane, not this answer.
//
// The plain form is still accepted when the caller does not know the epoch
// (minEpoch == 0), which is the case in the tests that pin the shape of a
// row.
func rowIsOurs(mt []string, minEpoch int) bool {
	if mt[2] == "" {
		return minEpoch == 0
	}
	ep, err := strconv.Atoi(mt[1])
	return err == nil && ep >= minEpoch
}

// numberedCandidates returns every place in the column where a numbered row
// could start.
//
// Why it exists. The screen buffer is the RAW STREAM, not a rendered
// screen: tmux moves the cursor with an escape sequence and writes the row
// where it lands, so a row can end up on the same stream line as the status
// bar that was repainted just before it:
//
//	^[[50;1H^[[2Kctrl+b:…ssh://opsuser@node-071^[[14;1H…^[[?25h1000-2@node
//
// The numbered row is at the END of that line, and the row matcher wants a
// column that STARTS with the number — so the row was dropped, the answer
// came back with an exit code and no output at all, and the pane looked
// healthy.
//
// Measured on 2026-10-28: one answer in sixteen was empty, on a fresh
// session (where the pane still repaints the status bar around the first
// command).
//
// The rule: a numbered row starts at a SEGMENT BOUNDARY — right after an
// escape sequence — and the FIRST such boundary that carries a row wins.
//
// The FIRST, not the last: the output itself can contain something that
// looks like a row. A colour-highlighted match (grep --color on "2024@foo")
// is a segment of its own, and taking the last match would let it take the
// row's place — the answer would carry the highlighted fragment instead of
// the command's output, silently.

// numberedCandidates returns every place in the column where a numbered row
// could start.
//
// Why it exists. The screen buffer is the RAW STREAM, not a rendered
// screen: tmux moves the cursor with an escape sequence and writes the row
// where it lands, so a row can end up on the same stream line as the status
// bar that was repainted just before it:
//
//	^[[50;1H^[[2Kctrl+b:…ssh://opsuser@node-071^[[14;1H…^[[?25h1000-2@node
//
// The numbered row is at the END of that line, and the row matcher wants a
// column that STARTS with the number — so the row was dropped, the answer
// came back with an exit code and no output at all, and the pane looked
// healthy.
//
// Measured on 2026-10-28: one answer in sixteen was empty, on a fresh
// session (where the pane still repaints the status bar around the first
// command).
//
// The rule: a numbered row starts at a SEGMENT BOUNDARY — right after an
// escape sequence — and the FIRST such boundary that carries a row wins.
//
// The FIRST, not the last: the output itself can contain something that
// looks like a row. A colour-highlighted match (grep --color on "2024@foo")
// is a segment of its own, and taking the last match would let it take the
// row's place — the answer would carry the highlighted fragment instead of
// the command's output, silently.
func numberedCandidates(col string) []rowCandidate {
	var out []rowCandidate
	for i := 0; i < len(col); {
		end := segmentEnd(col, i)
		if numberedLine.MatchString(col[i:end]) {
			out = append(out, rowCandidate{start: i})
		}
		i = end
	}
	return out
}

// segmentEnd returns the index just past the segment that starts at i.
//
// A segment ends where the next escape sequence begins (or at the end of
// the string), and the escape sequence is consumed WHOLE — including the
// shapes that are not CSI:
//
//   - ESC [ … final    a CSI (cursor moves, colours, the cursor style)
//   - ESC ] … BEL|ST   an OSC (the window title!) — left unconsumed, its
//     text would start the next segment, and the row after it would not
//     look like a row
//   - ESC ( B etc.     a charset selection — two bytes
//   - ESC M, ESC 7 …   a single-byte escape — two bytes. The second byte
//     has to be consumed as well: a digit there would otherwise start the
//     next segment looking like a row.

// segmentEnd returns the index just past the segment that starts at i.
//
// A segment ends where the next escape sequence begins (or at the end of
// the string), and the escape sequence is consumed WHOLE — including the
// shapes that are not CSI:
//
//   - ESC [ … final    a CSI (cursor moves, colours, the cursor style)
//   - ESC ] … BEL|ST   an OSC (the window title!) — left unconsumed, its
//     text would start the next segment, and the row after it would not
//     look like a row
//   - ESC ( B etc.     a charset selection — two bytes
//   - ESC M, ESC 7 …   a single-byte escape — two bytes. The second byte
//     has to be consumed as well: a digit there would otherwise start the
//     next segment looking like a row.
func segmentEnd(s string, i int) int {
	// The segment runs to the next escape sequence, and the escape sequence
	// is part of it: the text after the escape starts the NEXT segment,
	// which is what lets a row be found at the start of a segment.
	j := i
	for j < len(s) && s[j] != 0x1b {
		j++
	}
	if j >= len(s) {
		return len(s)
	}
	if j+1 >= len(s) {
		return len(s)
	}
	j++
	switch s[j] {
	case '[':
		// CSI: parameter bytes, then intermediate bytes, then a final byte.
		j++
		for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
			j++
		}
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		// The final byte is only consumed when it really is one: a
		// malformed sequence must not eat a byte of the text.
		if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
			j++
		}
		return j
	case ']':
		// OSC: the text runs to BEL or ST.
		for j++; j < len(s); j++ {
			if s[j] == 0x07 {
				j++
				break
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				j += 2
				break
			}
		}
		return j
	case '(', ')', '*', '+', '-', '.', '/':
		// A charset selection (ESC ( B): two bytes.
		return j + 2
	default:
		// A single-byte escape (ESC M, ESC 7, …): two bytes.
		return j + 1
	}
}

// numberedLine matches one "cat -n" line: leading spaces, the number, a tab,
// then the text.
// numberedLine matches one line of the `cat -n | tr '\t' '@'` pipeline:
// leading spaces, the line number, the '@' separator, then the text.
//
// The separator is the literal '@' because that is what Exec converts the tab
// into; matching whitespace here would fail against a real screen, where tmux
// has already collapsed the tab away.

// numberedLine matches one "cat -n" line: leading spaces, the number, a tab,
// then the text.
// numberedLine matches one line of the `cat -n | tr '\t' '@'` pipeline:
// leading spaces, the line number, the '@' separator, then the text.
//
// The separator is the literal '@' because that is what Exec converts the tab
// into; matching whitespace here would fail against a real screen, where tmux
// has already collapsed the tab away.
var numberedLine = regexp.MustCompile(`^\s*(\d+)(?:-(\d+))?@(.*)$`)

// cleanFrame removes the frame decoration from a painted row and returns its
// columns, each already trimmed.

// cleanFrame removes the frame decoration from a painted row and returns its
// columns, each already trimmed.
func cleanFrameColumns(row string) []string {
	var out []string
	for _, col := range frameSplit.Split(row, -1) {
		// Restore spaces only after splitting on frame bars, and only then trim:
		// while a space is still marked it is shielded from the Trim, which
		// would otherwise eat the padding tmux added after each column.
		col = strings.ReplaceAll(col, spaceMark, " ")
		col = strings.Trim(col, " ─━╭╮╰╯╔╗╚╝┌┐└┘\t")
		if col == "" {
			continue
		}
		if strings.Trim(col, "─━═╭╮╰╯╔╗╚╝┌┐└┘ ") == "" {
			continue
		}
		out = append(out, col)
	}
	return out
}

// Close exits the asset shell and tears the session down.

// ansiRe matches the escape sequences a terminal emits.
//
// The CSI part takes the intermediate bytes into account: "ESC[0 q" (the
// cursor style) is a CSI whose parameters are followed by a SPACE, and a
// pattern that only knows about digits leaves it in the text. That leak was
// hidden for a long time because a column starting with an escape sequence
// never matched the row matcher at all — the row was dropped, and the
// escape never reached an answer.
//
// The cost of taking the intermediate bytes: a payload that contains a bare
// "ESC[" followed by a space and a letter has those three characters eaten.
// It is a trade against leaving a cursor-style sequence in the answer, and
// the trade is worth it — a dropped row is a lost answer, while this costs
// three characters of an output that has to contain a raw escape to be
// affected at all.
var ansiRe = regexp.MustCompile(`\x1b\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][AB0]|\x1b[=>]`)

func stripANSI(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// tail is the LAST n characters of a string.
//
// It is not stringx.FirstN, which keeps the first n: an error message about
// a screen wants the END of the screen. The slice is by rune, so a
// multi-byte character is never cut in half.

// tail is the LAST n characters of a string.
//
// It is not stringx.FirstN, which keeps the first n: an error message about
// a screen wants the END of the screen. The slice is by rune, so a
// multi-byte character is never cut in half.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "..." + string(r[len(r)-n:])
}
