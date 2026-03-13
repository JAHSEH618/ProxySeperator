package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

const (
	defaultMaxFileSize   = 5 * 1024 * 1024 // 5 MB
	defaultMaxAge        = 7 * 24 * time.Hour
	defaultCleanInterval = 1 * time.Hour
	logFilePrefix        = "proxy-separator"
	logFileExt           = ".log"
	currentLogFile       = logFilePrefix + logFileExt
)

// FileSinkOptions configures the rotating file sink.
type FileSinkOptions struct {
	Dir         string        // Directory for log files.
	MaxFileSize int64         // Max bytes per file before rotation. Default 5 MB.
	MaxAge      time.Duration // Max age of rotated files. Default 7 days.
}

// FileSink writes log entries to a file with size-based rotation and
// time-based retention. Rotated files are named with a timestamp suffix.
type FileSink struct {
	mu          sync.Mutex
	dir         string
	maxFileSize int64
	maxAge      time.Duration
	file        *os.File
	written     int64
	stopClean   chan struct{}
}

// NewFileSink creates a rotating file sink. Call Close when done.
func NewFileSink(opts FileSinkOptions) (*FileSink, error) {
	if opts.Dir == "" {
		return nil, fmt.Errorf("log directory is required")
	}
	if opts.MaxFileSize <= 0 {
		opts.MaxFileSize = defaultMaxFileSize
	}
	if opts.MaxAge <= 0 {
		opts.MaxAge = defaultMaxAge
	}

	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}

	fs := &FileSink{
		dir:         opts.Dir,
		maxFileSize: opts.MaxFileSize,
		maxAge:      opts.MaxAge,
		stopClean:   make(chan struct{}),
	}

	if err := fs.openFile(); err != nil {
		return nil, err
	}

	// Initial cleanup of old files, then schedule periodic cleanup.
	fs.cleanOldFiles()
	go fs.cleanLoop()

	return fs, nil
}

// Sink returns a Sink function suitable for Logger.AddSink.
func (fs *FileSink) Sink() Sink {
	return fs.write
}

// Close flushes and closes the current log file and stops background cleanup.
func (fs *FileSink) Close() error {
	close(fs.stopClean)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.file != nil {
		err := fs.file.Close()
		fs.file = nil
		return err
	}
	return nil
}

func (fs *FileSink) write(entry api.LogEntry) {
	fields := ""
	if len(entry.Fields) > 0 {
		if payload, err := json.Marshal(entry.Fields); err == nil {
			fields = " " + string(payload)
		}
	}
	line := fmt.Sprintf(
		"%s [%s] %s %s%s\n",
		entry.Timestamp.Format(time.RFC3339),
		entry.Level,
		entry.Module,
		entry.Message,
		fields,
	)

	fs.mu.Lock()
	defer fs.mu.Unlock()

	if fs.file == nil {
		return
	}

	n, err := fs.file.WriteString(line)
	if err != nil {
		return
	}
	fs.written += int64(n)

	if fs.written >= fs.maxFileSize {
		fs.rotateFile()
	}
}

func (fs *FileSink) openFile() error {
	path := filepath.Join(fs.dir, currentLogFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	fs.file = f
	fs.written = info.Size()
	return nil
}

func (fs *FileSink) rotateFile() {
	if fs.file != nil {
		_ = fs.file.Close()
		fs.file = nil
	}

	src := filepath.Join(fs.dir, currentLogFile)
	stamp := time.Now().Format("2006-01-02T15-04-05")
	dst := filepath.Join(fs.dir, logFilePrefix+"-"+stamp+logFileExt)
	_ = os.Rename(src, dst)

	_ = fs.openFile()
}

func (fs *FileSink) cleanOldFiles() {
	entries, err := os.ReadDir(fs.dir)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-fs.maxAge)
	var rotated []string

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == currentLogFile {
			continue
		}
		if !strings.HasPrefix(name, logFilePrefix) || !strings.HasSuffix(name, logFileExt) {
			continue
		}
		rotated = append(rotated, name)
	}

	sort.Strings(rotated)
	for _, name := range rotated {
		path := filepath.Join(fs.dir, name)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}

func (fs *FileSink) cleanLoop() {
	ticker := time.NewTicker(defaultCleanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-fs.stopClean:
			return
		case <-ticker.C:
			fs.mu.Lock()
			fs.cleanOldFiles()
			fs.mu.Unlock()
		}
	}
}
