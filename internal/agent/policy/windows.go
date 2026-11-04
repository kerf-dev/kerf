package policy

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// InWindow reports whether now lies inside one of the windows. A recurring window (schedule +
// duration) is open for duration after each schedule activation; a one-off window is open in
// [start, end). An invalid window is reported as an error and treated as closed.
func InWindow(windows []v1alpha1.TimeWindow, now time.Time) (bool, error) {
	var firstErr error
	for i, w := range windows {
		open, err := windowOpen(w, now)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("window %d: %w", i, err)
			}
			continue
		}
		if open {
			return true, firstErr
		}
	}
	return false, firstErr
}

func windowOpen(w v1alpha1.TimeWindow, now time.Time) (bool, error) {
	if w.Schedule == "" {
		if w.Start == nil || w.End == nil {
			return false, fmt.Errorf("needs schedule+duration or start+end")
		}
		return !now.Before(w.Start.Time) && now.Before(w.End.Time), nil
	}
	if w.Duration == nil || w.Duration.Duration <= 0 {
		return false, fmt.Errorf("schedule %q needs a positive duration", w.Schedule)
	}
	loc := time.UTC
	if w.TimeZone != "" {
		l, err := time.LoadLocation(w.TimeZone)
		if err != nil {
			return false, fmt.Errorf("time zone %q: %w", w.TimeZone, err)
		}
		loc = l
	}
	sched, err := cronParser.Parse(w.Schedule)
	if err != nil {
		return false, fmt.Errorf("schedule %q: %w", w.Schedule, err)
	}
	// The window is open iff an activation happened in (now-duration, now].
	t := now.In(loc)
	next := sched.Next(t.Add(-w.Duration.Duration))
	return !next.After(t), nil
}
