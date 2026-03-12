package config

import (
	"testing"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

func TestMigrateFillsDefaults(t *testing.T) {
	cfg := api.Config{}
	changed := Migrate(&cfg)
	if !changed {
		t.Fatal("expected migration to report changes")
	}
	if cfg.Version == 0 || cfg.PersonalUpstream.Port == 0 {
		t.Fatalf("expected defaults to be filled, got %+v", cfg)
	}
	if cfg.CompanyUpstream.Protocol != api.ProtocolDirect {
		t.Fatalf("expected company traffic to default to direct system route, got %+v", cfg.CompanyUpstream)
	}
}
