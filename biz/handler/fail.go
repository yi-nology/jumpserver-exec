package handler

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
