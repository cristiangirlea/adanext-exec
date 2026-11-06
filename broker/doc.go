// Package broker is the Go SDK of adanext-exec, the gate agent shell commands are meant to go
// through. Run runs one Command, argv or shell text, with a timeout, and kills the whole
// process tree when it must stop. The rest arrives milestone by milestone, as docs/design.md
// and PLAN.md describe: the broker will decide (allow, ask or refuse) under a policy, add
// limits (a memory ceiling, how many heavy commands at once), and write one audit record per
// command.
//
// Programs in other languages will use the adanext-exec binary and its JSON protocol instead;
// ProtocolVersion names the protocol both speak.
package broker

// ProtocolVersion is the version of the JSON protocol between the adanext-exec daemon and its
// clients. A client sends it in its first message and the daemon refuses a version it does not
// speak, so the two can change independently.
const ProtocolVersion = "adanext-exec/1"
