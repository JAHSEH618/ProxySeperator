package logging

import (
	"sync"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

type Sink func(api.LogEntry)

type Logger struct {
	buffer *RingBuffer
	sinks  []Sink
	mu     sync.RWMutex
}

func NewLogger(buffer *RingBuffer) *Logger {
	if buffer == nil {
		buffer = NewRingBuffer(200)
	}
	return &Logger{buffer: buffer}
}

func (l *Logger) AddSink(sink Sink) {
	if sink == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sinks = append(l.sinks, sink)
}

func (l *Logger) Log(level, module, message string, fields map[string]any) api.LogEntry {
	entry := api.LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Module:    module,
		Message:   message,
		Fields:    fields,
	}
	l.buffer.Add(entry)

	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, sink := range l.sinks {
		sink(entry)
	}
	return entry
}

func (l *Logger) Debug(module, message string, fields map[string]any) api.LogEntry {
	return l.Log("DEBUG", module, message, fields)
}

func (l *Logger) Info(module, message string, fields map[string]any) api.LogEntry {
	return l.Log("INFO", module, message, fields)
}

func (l *Logger) Warn(module, message string, fields map[string]any) api.LogEntry {
	return l.Log("WARN", module, message, fields)
}

func (l *Logger) Error(module, message string, fields map[string]any) api.LogEntry {
	return l.Log("ERROR", module, message, fields)
}

func (l *Logger) List(limit int) []api.LogEntry {
	return l.buffer.List(limit)
}
