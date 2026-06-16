package brilliant

import (
	"testing"
)

func TestDomainInfo(t *testing.T) {
	info := Domain{}.Info()
	if info.Scheme != "brilliant" {
		t.Errorf("Scheme = %q, want brilliant", info.Scheme)
	}
	if len(info.Hosts) == 0 || info.Hosts[0] != Host {
		t.Errorf("Hosts = %v, want [%s]", info.Hosts, Host)
	}
	if info.Identity.Binary != "brilliant" {
		t.Errorf("Identity.Binary = %q, want brilliant", info.Identity.Binary)
	}
}

func TestDomainInfo_aliases(t *testing.T) {
	info := Domain{}.Info()
	found := false
	for _, a := range info.Aliases {
		if a == "br" {
			found = true
		}
	}
	if !found {
		t.Errorf("Aliases = %v, want to contain 'br'", info.Aliases)
	}
}
