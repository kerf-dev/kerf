// Package windows evaluates maintenance and freeze windows (WO-10).
//
// A window is recurring (5-field cron `schedule` + `duration`, evaluated in `timeZone`, default
// UTC) or one-off (`start` + `end`). Maintenance windows are the only times the `deferred`
// strategy applies changes; freeze windows block every change except health-watch rollbacks. A
// cluster-wide freeze (Freeze) applies on top of every policy and cannot be overridden by one.
package windows

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Open reports whether now lies inside one of the windows. A recurring window is open for
// duration after each schedule activation; a one-off window is open in [start, end). An invalid
// window is reported as an error and treated as closed.
func Open(windows []v1alpha1.TimeWindow, now time.Time) (bool, error) {
	var firstErr error
	for i, w := range windows {
		open, err := isOpen(w, now)
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

// Next returns when the next window opens after now (strictly later), and false if none will.
// Invalid windows are skipped.
func Next(windows []v1alpha1.TimeWindow, now time.Time) (time.Time, bool) {
	var best time.Time
	for _, w := range windows {
		t, ok := nextOpen(w, now)
		if ok && (best.IsZero() || t.Before(best)) {
			best = t
		}
	}
	return best, !best.IsZero()
}

// End returns when the open windows containing now close (the latest end among them, following
// overlapping windows), and false if none is open or one never closes.
func End(windows []v1alpha1.TimeWindow, now time.Time) (time.Time, bool) {
	end := now
	for range 32 { // follow chained windows, bounded
		next := end
		for _, w := range windows {
			if e, ok := closesAt(w, end); ok && e.After(next) {
				next = e
			}
		}
		if !next.After(end) {
			break
		}
		end = next
	}
	if !end.After(now) {
		return time.Time{}, false
	}
	return end, true
}

func location(w v1alpha1.TimeWindow) (*time.Location, error) {
	if w.TimeZone == "" {
		return time.UTC, nil
	}
	l, err := time.LoadLocation(w.TimeZone)
	if err != nil {
		return nil, fmt.Errorf("time zone %q: %w", w.TimeZone, err)
	}
	return l, nil
}

func schedule(w v1alpha1.TimeWindow) (cron.Schedule, *time.Location, error) {
	if w.Duration == nil || w.Duration.Duration <= 0 {
		return nil, nil, fmt.Errorf("schedule %q needs a positive duration", w.Schedule)
	}
	loc, err := location(w)
	if err != nil {
		return nil, nil, err
	}
	s, err := cronParser.Parse(w.Schedule)
	if err != nil {
		return nil, nil, fmt.Errorf("schedule %q: %w", w.Schedule, err)
	}
	return s, loc, nil
}

func isOpen(w v1alpha1.TimeWindow, now time.Time) (bool, error) {
	if w.Schedule == "" {
		if w.Start == nil || w.End == nil {
			return false, errors.New("needs schedule+duration or start+end")
		}
		return !now.Before(w.Start.Time) && now.Before(w.End.Time), nil
	}
	s, loc, err := schedule(w)
	if err != nil {
		return false, err
	}
	// Open iff an activation happened in (now-duration, now].
	t := now.In(loc)
	return !s.Next(t.Add(-w.Duration.Duration)).After(t), nil
}

func nextOpen(w v1alpha1.TimeWindow, now time.Time) (time.Time, bool) {
	if w.Schedule == "" {
		if w.Start == nil || w.End == nil || !w.Start.After(now) {
			return time.Time{}, false
		}
		return w.Start.Time, true
	}
	s, loc, err := schedule(w)
	if err != nil {
		return time.Time{}, false
	}
	return s.Next(now.In(loc)).UTC(), true
}

// closesAt returns when w closes if it is open at t.
func closesAt(w v1alpha1.TimeWindow, t time.Time) (time.Time, bool) {
	if open, err := isOpen(w, t); err != nil || !open {
		return time.Time{}, false
	}
	if w.Schedule == "" {
		return w.End.Time, true
	}
	s, loc, _ := schedule(w)
	// The activation that opened the window is the first one after t-duration.
	start := s.Next(t.In(loc).Add(-w.Duration.Duration))
	return start.Add(w.Duration.Duration).UTC(), true
}

// Freeze is the cluster-wide freeze configured on the agent (chart value `freeze`).
type Freeze struct {
	// Now freezes the cluster until it is turned off again.
	Now bool `json:"now,omitempty"`
	// Windows are recurring or one-off freeze windows for the whole cluster.
	Windows []v1alpha1.TimeWindow `json:"windows,omitempty"`
}

// LoadFreeze reads a Freeze from a YAML file; an empty path or missing file is no freeze.
func LoadFreeze(path string) (Freeze, error) {
	var f Freeze
	if path == "" {
		return f, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := yaml.UnmarshalStrict(b, &f); err != nil {
		return f, fmt.Errorf("freeze %s: %w", path, err)
	}
	for i, w := range f.Windows {
		if _, err := isOpen(w, time.Now()); err != nil {
			return f, fmt.Errorf("freeze %s: window %d: %w", path, i, err)
		}
	}
	return f, nil
}

// always is a one-off window that is open for the foreseeable future.
var always = v1alpha1.TimeWindow{
	Start: &metav1.Time{Time: time.Unix(0, 0).UTC()},
	End:   &metav1.Time{Time: time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)},
}

// Merge returns the policy's freeze windows plus the cluster-wide ones.
func (f Freeze) Merge(policy []v1alpha1.TimeWindow) []v1alpha1.TimeWindow {
	if !f.Now && len(f.Windows) == 0 {
		return policy
	}
	out := append(append([]v1alpha1.TimeWindow{}, policy...), f.Windows...)
	if f.Now {
		out = append(out, always)
	}
	return out
}
