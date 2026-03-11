package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

type recoveryJournal struct {
	path string
}

func newRecoveryJournal(path string) *recoveryJournal {
	return &recoveryJournal{path: path}
}

func (j *recoveryJournal) Exists() bool {
	if j == nil || j.path == "" {
		return false
	}
	_, err := os.Stat(j.path)
	return err == nil
}

func (j *recoveryJournal) Load() (api.RecoverySnapshot, error) {
	if j == nil || j.path == "" {
		return api.RecoverySnapshot{}, api.NewError(api.ErrCodeRecoveryFailed, "恢复 journal 路径未配置")
	}
	data, err := os.ReadFile(j.path)
	if err != nil {
		return api.RecoverySnapshot{}, api.WrapError(api.ErrCodeRecoveryFailed, "读取恢复 journal 失败", err)
	}
	var snapshot api.RecoverySnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return api.RecoverySnapshot{}, api.WrapError(api.ErrCodeRecoveryFailed, "解析恢复 journal 失败", err)
	}
	return snapshot, nil
}

func (j *recoveryJournal) Save(snapshot api.RecoverySnapshot) error {
	if j == nil || j.path == "" {
		return api.NewError(api.ErrCodeRecoveryFailed, "恢复 journal 路径未配置")
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
		return api.WrapError(api.ErrCodeRecoveryFailed, "创建恢复目录失败", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return api.WrapError(api.ErrCodeRecoveryFailed, "序列化恢复 journal 失败", err)
	}
	if err := os.WriteFile(j.path, data, 0o644); err != nil {
		return api.WrapError(api.ErrCodeRecoveryFailed, "写入恢复 journal 失败", err)
	}
	return nil
}

func (j *recoveryJournal) Remove() error {
	if j == nil || j.path == "" {
		return nil
	}
	if err := os.Remove(j.path); err != nil && !os.IsNotExist(err) {
		return api.WrapError(api.ErrCodeRecoveryFailed, "删除恢复 journal 失败", err)
	}
	return nil
}
