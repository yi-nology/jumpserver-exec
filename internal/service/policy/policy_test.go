package policy

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yi-nology/jumpserver-kit/model/jms"

	"github.com/yi-nology/jumpserver-exec/biz/handler"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/testsupport"
)

// The catalogue and the registration must agree: a tool that is registered
// but not classified would be treated as an unclassified write, and a tool
// that is classified but never registered would leave a rule nobody can
// reach.
func TestEveryRegisteredToolIsClassified(t *testing.T) {
	r := reg.New(&mcp.Implementation{Name: "exec-policy-test", Version: "1"},
		reg.Options{Policy: Table})
	c := mustLoad(t).APIClient()
	handler.RegisterExecTools(r, mustLoad(t))
	handler.RegisterTransferTools(r, mustLoad(t))
	handler.RegisterAuditTools(r, mustLoad(t).Audit)
	if c != nil {
		handler.RegisterInventoryTools(r, c)
	}
	for _, spec := range r.Specs() {
		p, ok := Table[spec.Name]
		if !ok {
			t.Errorf("tool %s is registered but not classified", spec.Name)
			continue
		}
		if p.Risk != spec.Risk {
			t.Errorf("tool %s: policy risk %s, registered risk %s", spec.Name, p.Risk, spec.Risk)
		}
	}
}

// Every tool must say what its answer looks like.
//
// The declaration is what the server checks the answer against before the
// model sees it, so a tool that declares nothing is a tool whose shape can
// drift without anybody noticing.
func TestEveryToolDeclaresItsOutputShape(t *testing.T) {
	r := reg.New(&mcp.Implementation{Name: "exec-policy-test", Version: "1"},
		reg.Options{Policy: Table})
	c := mustLoad(t).APIClient()
	handler.RegisterExecTools(r, mustLoad(t))
	handler.RegisterTransferTools(r, mustLoad(t))
	handler.RegisterAuditTools(r, mustLoad(t).Audit)
	if c != nil {
		handler.RegisterInventoryTools(r, c)
	}
	for _, spec := range r.Specs() {
		if !r.HasOutputSchema(spec.Name) {
			t.Errorf("tool %s declares no output schema; its answer is unchecked", spec.Name)
		}
	}
}

func TestEveryClassifiedToolIsRegistered(t *testing.T) {
	r := reg.New(&mcp.Implementation{Name: "exec-policy-test", Version: "1"},
		reg.Options{Policy: Table})
	c := mustLoad(t).APIClient()
	handler.RegisterExecTools(r, mustLoad(t))
	handler.RegisterTransferTools(r, mustLoad(t))
	handler.RegisterAuditTools(r, mustLoad(t).Audit)
	if c != nil {
		handler.RegisterInventoryTools(r, c)
	}
	registered := map[string]bool{}
	for _, spec := range r.Specs() {
		registered[spec.Name] = true
	}
	for name := range Table {
		// The inventory tool needs the REST client; without a token it is
		// not registered at all, and that is the intended behaviour.
		if name == "jmsx_inventory" && c == nil {
			continue
		}
		if !registered[name] {
			t.Errorf("tool %s is classified but never registered", name)
		}
	}
}

// A tool that acts on the machine asks for a confirmation.
//
// The management plane's rule is the same: anything that changes something
// states the consequence and waits for the flag. The sentence is what the
// caller is told when the call is refused.
func TestEveryWriteToolCarriesItsConfirmationSentence(t *testing.T) {
	for name, p := range Table {
		if p.Risk == jms.Risk_read {
			if p.Confirm != "" {
				t.Errorf("read tool %s carries a confirmation sentence", name)
			}
			continue
		}
		if p.Confirm == "" {
			// A conditional confirmation has no sentence to refuse with —
			// the tool decides when the flag is needed.
			if !p.Conditional {
				t.Errorf("write tool %s has no confirmation sentence", name)
			} else if p.Hint == "" {
				t.Errorf("write tool %s has a conditional confirmation but no hint for the catalogue", name)
			}
			continue
		}
		if !strings.Contains(p.Confirm, "confirm=true") {
			t.Errorf("tool %s: confirmation sentence does not mention the flag: %q", name, p.Confirm)
		}
	}
}

// The tool count is the number the skills quote. A change here is a change to
// the contract with the agent.
func TestToolCount(t *testing.T) {
	r := reg.New(&mcp.Implementation{Name: "exec-policy-test", Version: "1"},
		reg.Options{Policy: Table})
	c := mustLoad(t).APIClient()
	handler.RegisterExecTools(r, mustLoad(t))
	handler.RegisterTransferTools(r, mustLoad(t))
	handler.RegisterAuditTools(r, mustLoad(t).Audit)
	if c != nil {
		handler.RegisterInventoryTools(r, c)
	}
	// The NAMES are asserted, not just the count. A count says "something
	// changed" without saying what; the point of this check is the
	// contract with the agent — the set of tools it may call — and a set
	// does not rot the way a number does.
	//
	// The inventory tool is registered only when there is an API client,
	// so it is the one name whose presence depends on the wiring.
	want := map[string]bool{
		"jmsx_exec": true, "jmsx_run": true, "jmsx_collect": true,
		"jmsx_collect_multi": true, "jmsx_exec_multi": true,
		"jmsx_bg_start": true, "jmsx_bg_check": true,
		"jmsx_put_file": true, "jmsx_get_file": true, "jmsx_relay": true,
		"jmsx_reachable": true, "jmsx_session_cost": true,
		"jmsx_pool_status": true, "jmsx_audit_query": true,
	}
	expected := len(want)
	for _, spec := range r.Specs() {
		delete(want, spec.Name)
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for name := range want {
			missing = append(missing, name)
		}
		sort.Strings(missing)
		t.Errorf("these tools are not registered: %s", strings.Join(missing, ", "))
	}
	// The count follows the set, not a number someone has to remember to
	// bump: the inventory tool is registered only when there is an API
	// client, so the count is the set's size with or without it.
	if got, want := len(r.Specs()), expected+1; got != want && got != want-1 {
		t.Errorf("tool count = %d, want %d (or %d without the inventory tool)", got, want, want-1)
	}
}

// The read-only mode refuses the tools that change the machine.
//
// This is a mode of the gate, not a convention in the handlers — and it has
// to be wired from the configuration, which is exactly the place where it
// was once documented but not connected.
func TestReadOnlyModeRefusesWrites(t *testing.T) {
	cs := testsupport.NewSession(t, "exec-readonly-test",
		reg.Options{Policy: Table, ReadOnly: true},
		func(r *reg.Registry) { handler.RegisterExecTools(r, mustLoad(t)) })

	// The confirmation is there, and the call is still refused: the mode
	// comes first.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "jmsx_exec",
		Arguments: map[string]any{"asset": "10.0.0.1", "command": "true", "confirm": true},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("the read-only mode let a write through")
	}
}

// mustLoad reads the execution plane's configuration, failing the test if it
// cannot be read.
func mustLoad(t *testing.T) config.Config {
	t.Helper()
	return testsupport.MustLoad(t, config.Load)
}
