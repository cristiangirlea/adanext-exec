// Package broker is the Go SDK of adanext-exec, the gate agent shell commands are meant to go
// through. Today it holds only ProtocolVersion. The rest arrives milestone by milestone, as
// docs/design.md and PLAN.md describe: a caller will hand the broker a command, and it will
// decide (allow, ask or refuse) under a policy, run the command within limits (a timeout, a
// memory ceiling, how many heavy commands at once), kill the whole process tree when it must
// stop, and write one audit record per command.
//
// Programs in other languages will use the adanext-exec binary and its JSON protocol instead;
// ProtocolVersion names the protocol both speak.
package broker

// ProtocolVersion is the version of the JSON protocol between the adanext-exec daemon and its
// clients. A client sends it in its first message and the daemon refuses a version it does not
// speak, so the two can change independently.
const ProtocolVersion = "adanext-exec/1"
