package policy

import (
	"time"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/windows"
)

// InWindow reports whether now lies inside one of the windows (see windows.Open).
func InWindow(ws []v1alpha1.TimeWindow, now time.Time) (bool, error) {
	return windows.Open(ws, now)
}
