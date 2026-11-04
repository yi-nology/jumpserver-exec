package handler

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
)

// fail hands the error back AS IT IS.
//
// The execution plane's errors can carry a delivery state: did the command
// reach the machine, or is it maybe still running there? That state is the
// difference between "safe to run again" and "never run it again", and it
// lives on the error — so flattening the error into a string throws it
// away, and the trail ends up saying "it failed" without saying the part
// that matters.
//
// The agent sees exactly the same text either way: the registry turns the
// error back into an error result with the error's own text.
//
// ⚠️ Only for errors that CARRY a state (the ones from the session layer).
// A failure of the tool's own input check has no delivery state to keep,
// and `render.Failf` says it better.
func fail(err error) (*mcp.CallToolResult, any, error) {
	return nil, nil, err
}

// errFields fills an answer's error path from a typed error: the error's own
// text, and — when the error knows it — its delivery state.
//
// This is the pair every answer's error path writes, and it is the same pair
// `fail` keeps for the error-result path: the state must survive the trip to
// the caller whichever way the failure is reported.
func errFields(err error) (msg *string, delivery *string) {
	if d := reg.DeliveryOf(err); d != jms.Delivery(0) {
		return jms.Opt(err.Error()), jms.Opt(d.String())
	}
	return jms.Opt(err.Error()), nil
}
