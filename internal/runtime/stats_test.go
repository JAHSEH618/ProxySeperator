package runtime

import (
	"testing"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

func TestStatsTrackerSnapshot(t *testing.T) {
	stats := NewStatsTracker()
	stats.Start(api.ModeSystem)
	stats.SessionStarted()
	stats.AddRX(128, api.RouteTargetCompany)
	stats.AddTX(64, api.RouteTargetPersonal)

	time.Sleep(20 * time.Millisecond)
	snapshot := stats.Snapshot(api.ModeSystem)
	if snapshot.ActiveSessions != 1 {
		t.Fatalf("expected active session 1, got %d", snapshot.ActiveSessions)
	}
	if snapshot.RXBytes != 128 || snapshot.TXBytes != 64 {
		t.Fatalf("unexpected bytes: %+v", snapshot)
	}
	if snapshot.CompanyBytes != 128 || snapshot.PersonalBytes != 64 {
		t.Fatalf("unexpected upstream bytes: %+v", snapshot)
	}
	stats.SessionEnded()
	stats.Stop()
}
