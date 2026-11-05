package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every fmt.Sprintf that feeds s.Run interpolates ONLY quoted (or literal)
// pieces.
//
// Why a scan and not a type rule: the interpolation happens in strings the
// compiler sees as ordinary expressions — `chmod %s %s` with args.Mode raw
// compiles fine, and a `; rm -rf` smuggled through it runs OUTSIDE every
// gate and trail this repo has (the risk classifier only ever sees the
// tool's name). That bug shipped once (jmsx_put_file's mode, jmsx_bg_check's
// path — both fixed in the same round this guard landed); the guard exists
// so the next one cannot.
//
// The scan is heuristic ON PURPOSE — it errs toward false positives, and a
// hit is resolved by quoting the value (the right fix), not by disabling
// the scan. Allowed shapes:
//
//   - literals:        fmt.Sprintf("rm -f %s", literal)
//   - quoted values:   fmt.Sprintf("chmod %s", shellx.Quote(args.Mode))
//   - quoted builds:   fmt.Sprintf("... %s ...", shellx.Quote(a)+" "+shellx.Quote(b))
//
// Anything else — a bare args.X, a variable that cannot be traced to a
// Quote call in the same function — is a finding.
func TestEverySprintfIntoRunInterpolatesQuotedOrLiteral(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// one fmt.Sprintf(...) call: arguments up to the matching close paren
	// at bracket depth 0 (a flat scan — nested parens inside an argument
	// are counted).
	sprintfRe := regexp.MustCompile(`fmt\.Sprintf\(`)
	// pieces of an argument expression that make it safe
	quoteRe := regexp.MustCompile(`shellx\.Quote\(`)
	literalRe := regexp.MustCompile(`^\s*"`)
	fieldRe := regexp.MustCompile(`args\.[A-Z]\w*`)

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			loc := sprintfRe.FindStringIndex(line)
			if loc == nil {
				continue
			}
			// Collect the full call (may span lines) up to the balancing
			// close paren.
			call := line[loc[1]-1:]
			depth := 0
			end := -1
			for j, r := range call {
				switch r {
				case '(':
					depth++
				case ')':
					depth--
					if depth == 0 {
						end = j
					}
				}
				if end >= 0 {
					break
				}
			}
			// continue collecting lines until balanced
			k := i
			for end < 0 && k+1 < len(lines) {
				k++
				call += lines[k]
				for j, r := range lines[k] {
					switch r {
					case '(':
						depth++
					case ')':
						depth--
						if depth == 0 {
							end = len(call) - len(lines[k]) + j
						}
					}
					if end >= 0 {
						break
					}
				}
			}
			if end < 0 {
				continue // unbalanced (not a real call) — the compiler owns it
			}
			// call[0] is the Sprintf's own '('; [1:end] is the argument
			// list — keeping the leading paren here would start the
			// top-level split at depth 1 and the commas would never split,
			// which is exactly the hole this guard exists to close.
			argsText := call[1:end]
			// Only calls that feed s.Run matter; Sprintf for anything else
			// (render messages, log lines) is out of scope.
			tail := ""
			if i+len(strings.Split(call[:end], "\n"))-1+1 < len(lines) {
				tail = strings.Join(lines[i:i+len(strings.Split(call[:end], "\n"))+2], " ")
			} else {
				tail = strings.Join(lines[i:], " ")
			}
			if !strings.Contains(tail, ".Run(") {
				continue
			}
			// Split top-level arguments (depth-0 commas).
			var parts []string
			depth, start := 0, 0
			for j, r := range argsText {
				switch r {
				case '(', '[', '{':
					depth++
				case ')', ']', '}':
					depth--
				case ',':
					if depth == 0 {
						parts = append(parts, argsText[start:j])
						start = j + 1
					}
				}
			}
			parts = append(parts, argsText[start:])
			// The escape hatch is IN BAND and reviewable: a value whose
			// shell meaning is "a whole command line, splitting intended"
			// (bg_start's wrapper) marks one of the call's lines
			// //shellquote:ok. An unmarked hit is always a fix-the-code,
			// never a disable-the-guard.
			if strings.Contains(call, "//shellquote:ok") {
				continue
			}
			for _, arg := range parts[1:] { // [0] is the format string
				arg = strings.TrimSpace(arg)
				if arg == "" || literalRe.MatchString(arg) {
					continue
				}
				if quoteRe.MatchString(arg) {
					continue
				}
				// A value built FROM quoted pieces is fine
				// (shellx.Quote(a) + " " + shellx.Quote(b)).
				if fieldRe.MatchString(arg) {
					t.Errorf("%s:%d: unquoted args field interpolated into a shell line: %s\n"+
						"wrap it in shellx.Quote(...) — the risk classifier only sees the tool's name, "+
						"so a smuggled command runs outside every gate", f, i+1, arg)
				}
				// A bare local variable is a finding too: the guard cannot
				// trace it, and tracing-by-eye is what let the two bugs in.
				// If the value is genuinely safe (already quoted upstream),
				// route it through shellx.Quote again — quoting is
				// idempotent for a quoted word and cheap everywhere else.
				t.Errorf("%s:%d: untraced variable interpolated into a shell line: %s\n"+
					"wrap it in shellx.Quote(...) so the guard can see it", f, i+1, arg)
			}
		}
	}
}
