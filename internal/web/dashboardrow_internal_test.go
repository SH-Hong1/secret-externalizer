package web

import (
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/dashboard"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

func TestNewDashboardRow_LastTime(t *testing.T) {
	requested := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	finished := time.Date(2026, 11, 20, 9, 18, 0, 0, time.UTC)
	tests := []struct {
		name string
		last scan.Scan
		want string
	}{
		{"finished scan shows when it finished", scan.Scan{ID: 8, Status: scan.StatusSucceeded, RequestedAt: requested, FinishedAt: finished}, "2026-11-20 09:18:00 UTC"},
		{"queued scan shows when it was requested", scan.Scan{ID: 8, Status: scan.StatusQueued, RequestedAt: requested}, "2026-11-20 09:00:00 UTC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := newDashboardRow(dashboard.Row{Last: tt.last}, 0, "")
			if row.Last != tt.want {
				t.Errorf("newDashboardRow(Last: %+v).Last = %q, want %q", tt.last, row.Last, tt.want)
			}
		})
	}
}
