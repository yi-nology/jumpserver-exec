// Command jumpserver-mcp-exec runs commands and moves files on machines managed by
// JumpServer, through the bastion's koko TUI — so every session is recorded
// and auditable.
//
// Transport: stdio, the same as the management plane. Credentials: an RSA
// SSH key registered as a JumpServer user (koko rejects password auth for
// automation) plus a PrivateToken for the read-only inventory tool.
//
// This is the command line of the execution plane. The normal mode is
// serving MCP over stdio: the agent runtime starts the binary with no
// arguments and speaks JSON-RPC over stdin/stdout. The subcommands exist for
// the moments when there is no session yet — the wiring is suspect and the
// tools are not answering.
//
// The exit codes are oekit's: 0 ok, 1 error, 2 usage.
//
// The repository is one module with two binaries; this is the execution
// plane's entry point. The layout follows the CloudWeGo hz convention:
//
//	cmd/jumpserver-mcp-exec/         the command line (this file)
//	conf/exec/                the configuration
//	idl/jmsexec/              the contract, in thrift
//	biz/exec/model/jmsexec/   the contract, generated from the IDL
//	biz/exec/handler/         the tools
//	biz/exec/policy/          the tool catalogue
//	biz/exec/router/          the wiring
//	internal/exec/koko/       the koko TUI protocol and the file transfer
//
// The shared base (the protocol layer, the gate, the result shaping) is at the
// repository root under pkg/.

package main

func main() {
	dispatch()
}
