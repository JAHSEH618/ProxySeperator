package config

import "github.com/friedhelmliu/ProxySeperator/internal/api"

type StoreConfig = api.Config

func Default() api.Config {
	return api.DefaultConfig()
}
