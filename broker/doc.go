// Package broker is the Go SDK of adanext-exec: the gate every agent shell command goes
// through. A caller hands it a command and the broker decides (allow, ask or refuse) under a
// policy, runs it within limits (how many heavy commands at once, a timeout, a memory ceiling),
// kills the whole process tree when it must stop, and writes one audit record per command.
//
// The package grows slice by slice as docs/design.md and PLAN.md describe. Programs in other
// languages use the adanext-exec binary and its JSON protocol instead; ProtocolVersion names
// the protocol both speak.
package broker

// ProtocolVersion is the version of the JSON protocol between the adanext-exec daemon and its
// clients. A client sends it in its first message and the daemon refuses a version it does not
// speak, so the two can change independently.
const ProtocolVersion = "adanext-exec/1"
