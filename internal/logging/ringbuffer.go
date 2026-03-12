package logging

import (
	"sync"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

type RingBuffer struct {
	mu       sync.RWMutex
	items    []api.LogEntry
	capacity int
	next     int
	full     bool
}

func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 200
	}
	return &RingBuffer{
		items:    make([]api.LogEntry, capacity),
		capacity: capacity,
	}
}

func (r *RingBuffer) Add(entry api.LogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[r.next] = entry
	r.next = (r.next + 1) % r.capacity
	if r.next == 0 {
		r.full = true
	}
}

func (r *RingBuffer) List(limit int) []api.LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := r.next
	if r.full {
		count = r.capacity
	}
	if limit <= 0 || limit > count {
		limit = count
	}
	result := make([]api.LogEntry, 0, limit)
	for i := 0; i < limit; i++ {
		index := (r.next - 1 - i + r.capacity) % r.capacity
		if !r.full && index >= count {
			continue
		}
		result = append(result, r.items[index])
	}

	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}
