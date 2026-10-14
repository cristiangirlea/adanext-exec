package broker

import (
	"strings"
	"testing"
)

func TestTheProtocolVersionNamesTheProtocolAndItsMajorVersion(t *testing.T) {
	name, major, ok := strings.Cut(ProtocolVersion, "/")
	if !ok || name != "adanext-exec" || major == "" || strings.ContainsAny(major, " .") {
		t.Errorf("ProtocolVersion = %q, want adanext-exec/<major>", ProtocolVersion)
	}
}
