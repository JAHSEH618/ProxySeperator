package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

type Store struct {
	appName string
}

func NewStore(appName string) *Store {
	if appName == "" {
		appName = api.AppName
	}
	return &Store{appName: appName}
}

func (s *Store) ConfigDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", api.WrapError(api.ErrCodeConfigLoadFailed, "无法获取用户配置目录", err)
	}
	return filepath.Join(dir, s.appName), nil
}

func (s *Store) ConfigPath() (string, error) {
	dir, err := s.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func (s *Store) ensureDir() (string, error) {
	dir, err := s.ConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", api.WrapError(api.ErrCodeConfigSaveFailed, "无法创建配置目录", err)
	}
	return dir, nil
}

func (s *Store) Load() (api.Config, error) {
	path, err := s.ConfigPath()
	if err != nil {
		return api.Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), nil
		}
		return api.Config{}, api.WrapError(api.ErrCodeConfigLoadFailed, "读取配置文件失败", err)
	}

	cfg := api.DefaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return api.Config{}, api.WrapError(api.ErrCodeConfigLoadFailed, "解析配置文件失败", err)
	}
	changed := Migrate(&cfg)
	if changed {
		_ = s.Save(cfg)
	}
	return cfg, nil
}

func (s *Store) Save(cfg api.Config) error {
	if _, err := s.ensureDir(); err != nil {
		return err
	}
	cfg.Version = api.DefaultConfig().Version
	path, err := s.ConfigPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return api.WrapError(api.ErrCodeConfigSaveFailed, "序列化配置失败", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return api.WrapError(api.ErrCodeConfigSaveFailed, "写入配置文件失败", err)
	}
	return nil
}
