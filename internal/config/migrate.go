package config

import "github.com/friedhelmliu/ProxySeperator/internal/api"

func Migrate(cfg *api.Config) bool {
	if cfg == nil {
		return false
	}
	changed := false
	defaults := api.DefaultConfig()

	if cfg.Version == 0 {
		cfg.Version = defaults.Version
		changed = true
	}
	if cfg.CompanyUpstream.Host == "" {
		cfg.CompanyUpstream.Host = defaults.CompanyUpstream.Host
		changed = true
	}
	if cfg.CompanyUpstream.Port == 0 {
		cfg.CompanyUpstream.Port = defaults.CompanyUpstream.Port
		changed = true
	}
	if cfg.CompanyUpstream.Protocol == "" {
		cfg.CompanyUpstream.Protocol = defaults.CompanyUpstream.Protocol
		changed = true
	}
	if cfg.PersonalUpstream.Host == "" {
		cfg.PersonalUpstream.Host = defaults.PersonalUpstream.Host
		changed = true
	}
	if cfg.PersonalUpstream.Port == 0 {
		cfg.PersonalUpstream.Port = defaults.PersonalUpstream.Port
		changed = true
	}
	if cfg.PersonalUpstream.Protocol == "" {
		cfg.PersonalUpstream.Protocol = defaults.PersonalUpstream.Protocol
		changed = true
	}
	if cfg.Advanced.Mode == "" {
		cfg.Advanced.Mode = defaults.Advanced.Mode
		changed = true
	}
	if cfg.UI.Language == "" {
		cfg.UI.Language = defaults.UI.Language
		changed = true
	}
	if cfg.UI.Theme == "" {
		cfg.UI.Theme = defaults.UI.Theme
		changed = true
	}
	if cfg.Rules == nil {
		cfg.Rules = append([]string(nil), defaults.Rules...)
		changed = true
	}
	return changed
}
