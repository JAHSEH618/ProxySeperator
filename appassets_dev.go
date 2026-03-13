//go:build !production

package appassets

import (
	"io/fs"
	"os"
	"path/filepath"
)

// FS returns the development asset filesystem backed by frontend/dist.
func FS() fs.FS {
	devPath := filepath.Join("frontend", "dist")
	if _, err := os.Stat(filepath.Join(devPath, "index.html")); err == nil {
		return os.DirFS(devPath)
	}

	if executable, err := os.Executable(); err == nil {
		bundledPath := filepath.Join(filepath.Dir(executable), "frontend", "dist")
		if _, err := os.Stat(filepath.Join(bundledPath, "index.html")); err == nil {
			return os.DirFS(bundledPath)
		}
	}

	return os.DirFS(devPath)
}
