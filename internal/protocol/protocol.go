// Package protocol defines the wire contract shared by the CLI and server:
// exit codes, the JSON response envelope, and (later) the SSH command
// tokenizer.
package protocol

// Version is the control-plane protocol version. It is embedded in every JSON
// response envelope; clients check it opportunistically and refuse on major
// mismatch. Bump the major on breaking envelope or command changes.
const Version = 1

// Exit codes shared by the CLI and by control commands run over bare ssh.
const (
	ExitOK       = 0
	ExitFailure  = 1 // general failure
	ExitUsage    = 2 // usage error
	ExitNotFound = 3
	ExitDenied   = 4
	ExitProtocol = 5 // server/protocol error
)

// Envelope wraps every JSON response from a control command.
type Envelope struct {
	ProtocolVersion int    `json:"protocol_version"`
	Data            any    `json:"data,omitempty"`
	Error           string `json:"error,omitempty"`
}
