package daemon_test

import (
	"testing"

	"github.com/cristiangirlea/adanext-exec/broker"
	"github.com/cristiangirlea/adanext-exec/internal/daemon"
)

// The daemon keeps its own copy of the version, so the SDK can import it; the two must agree.
func TestTheVersionIsTheSDKs(t *testing.T) {
	if daemon.Version != broker.ProtocolVersion {
		t.Errorf("daemon.Version = %q, broker.ProtocolVersion = %q", daemon.Version, broker.ProtocolVersion)
	}
}
