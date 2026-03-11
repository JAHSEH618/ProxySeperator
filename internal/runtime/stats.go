package runtime

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

type StatsTracker struct {
	startedAt atomic.Pointer[time.Time]

	activeSessions atomic.Int64
	totalSessions  atomic.Int64

	rxBytes       atomic.Uint64
	txBytes       atomic.Uint64
	companyBytes  atomic.Uint64
	personalBytes atomic.Uint64

	mu sync.Mutex

	lastSnapshotAt time.Time
	lastRX         uint64
	lastTX         uint64
	lastCompany    uint64
	lastPersonal   uint64

	lastComputed api.TrafficStats
}

func NewStatsTracker() *StatsTracker {
	return &StatsTracker{}
}

func (s *StatsTracker) Start(mode string) {
	now := time.Now()
	s.startedAt.Store(&now)
	s.activeSessions.Store(0)
	s.totalSessions.Store(0)
	s.rxBytes.Store(0)
	s.txBytes.Store(0)
	s.companyBytes.Store(0)
	s.personalBytes.Store(0)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSnapshotAt = now
	s.lastRX = 0
	s.lastTX = 0
	s.lastCompany = 0
	s.lastPersonal = 0
	s.lastComputed = api.TrafficStats{Mode: mode, StartedAt: &now}
}

func (s *StatsTracker) Stop() {
	s.activeSessions.Store(0)
}

func (s *StatsTracker) SessionStarted() {
	s.activeSessions.Add(1)
	s.totalSessions.Add(1)
}

func (s *StatsTracker) SessionEnded() {
	s.activeSessions.Add(-1)
}

func (s *StatsTracker) AddRX(n uint64, target string) {
	s.rxBytes.Add(n)
	switch target {
	case api.RouteTargetCompany:
		s.companyBytes.Add(n)
	case api.RouteTargetPersonal:
		s.personalBytes.Add(n)
	}
}

func (s *StatsTracker) AddTX(n uint64, target string) {
	s.txBytes.Add(n)
	switch target {
	case api.RouteTargetCompany:
		s.companyBytes.Add(n)
	case api.RouteTargetPersonal:
		s.personalBytes.Add(n)
	}
}

func (s *StatsTracker) Snapshot(mode string) api.TrafficStats {
	rx := s.rxBytes.Load()
	tx := s.txBytes.Load()
	company := s.companyBytes.Load()
	personal := s.personalBytes.Load()

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(s.lastSnapshotAt).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}

	startedAt := s.startedAt.Load()
	stats := api.TrafficStats{
		Mode:                   mode,
		StartedAt:              startedAt,
		ActiveSessions:         s.activeSessions.Load(),
		TotalSessions:          s.totalSessions.Load(),
		RXBytes:                rx,
		TXBytes:                tx,
		CompanyBytes:           company,
		PersonalBytes:          personal,
		RXBytesPerSecond:       float64(rx-s.lastRX) / elapsed,
		TXBytesPerSecond:       float64(tx-s.lastTX) / elapsed,
		CompanyBytesPerSecond:  float64(company-s.lastCompany) / elapsed,
		PersonalBytesPerSecond: float64(personal-s.lastPersonal) / elapsed,
	}

	s.lastSnapshotAt = now
	s.lastRX = rx
	s.lastTX = tx
	s.lastCompany = company
	s.lastPersonal = personal
	s.lastComputed = stats
	return stats
}
