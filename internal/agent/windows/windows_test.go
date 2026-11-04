package windows

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

var now = time.Date(2026, 11, 4, 10, 30, 0, 0, time.UTC) // Wednesday

func dur(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }
func at(t time.Time) *metav1.Time          { return &metav1.Time{Time: t} }

func TestOpen(t *testing.T) {
	tests := []struct {
		name    string
		w       v1alpha1.TimeWindow
		want    bool
		wantErr bool
	}{
		{"recurring open", v1alpha1.TimeWindow{Schedule: "0 10 * * *", Duration: dur(time.Hour)}, true, false},
		{"recurring closed", v1alpha1.TimeWindow{Schedule: "0 12 * * *", Duration: dur(time.Hour)}, false, false},
		{"recurring at end is closed", v1alpha1.TimeWindow{Schedule: "30 9 * * *", Duration: dur(time.Hour)}, false, false},
		{"weekday", v1alpha1.TimeWindow{Schedule: "0 0 * * 3", Duration: dur(24 * time.Hour)}, true, false},
		{"time zone", v1alpha1.TimeWindow{Schedule: "0 12 * * *", Duration: dur(time.Hour), TimeZone: "Asia/Jerusalem"}, true, false},
		{"one-off open", v1alpha1.TimeWindow{Start: at(now.Add(-time.Minute)), End: at(now.Add(time.Minute))}, true, false},
		{"one-off past", v1alpha1.TimeWindow{Start: at(now.Add(-2 * time.Hour)), End: at(now.Add(-time.Hour))}, false, false},
		{"bad cron", v1alpha1.TimeWindow{Schedule: "nope", Duration: dur(time.Hour)}, false, true},
		{"no duration", v1alpha1.TimeWindow{Schedule: "0 10 * * *"}, false, true},
		{"bad zone", v1alpha1.TimeWindow{Schedule: "0 10 * * *", Duration: dur(time.Hour), TimeZone: "Mars/Olympus"}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Open([]v1alpha1.TimeWindow{tt.w}, now)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Errorf("Open = %v, %v; want %v, err %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestNext(t *testing.T) {
	tests := []struct {
		name string
		ws   []v1alpha1.TimeWindow
		want time.Time
		ok   bool
	}{
		{"daily later today", []v1alpha1.TimeWindow{{Schedule: "0 22 * * *", Duration: dur(time.Hour)}}, time.Date(2026, 11, 4, 22, 0, 0, 0, time.UTC), true},
		{"daily tomorrow", []v1alpha1.TimeWindow{{Schedule: "0 2 * * *", Duration: dur(time.Hour)}}, time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC), true},
		{"open now: the next opening", []v1alpha1.TimeWindow{{Schedule: "0 10 * * *", Duration: dur(time.Hour)}}, time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC), true},
		{"time zone", []v1alpha1.TimeWindow{{Schedule: "0 2 * * *", Duration: dur(time.Hour), TimeZone: "Asia/Jerusalem"}}, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), true},
		{"earliest of several", []v1alpha1.TimeWindow{
			{Schedule: "0 22 * * *", Duration: dur(time.Hour)},
			{Start: at(now.Add(2 * time.Minute)), End: at(now.Add(4 * time.Minute))},
		}, now.Add(2 * time.Minute), true},
		{"one-off in the past", []v1alpha1.TimeWindow{{Start: at(now.Add(-time.Hour)), End: at(now.Add(time.Hour))}}, time.Time{}, false},
		{"invalid skipped", []v1alpha1.TimeWindow{{Schedule: "x", Duration: dur(time.Hour)}}, time.Time{}, false},
		{"none", nil, time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Next(tt.ws, now)
			if ok != tt.ok || !got.Equal(tt.want) {
				t.Errorf("Next = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestEnd(t *testing.T) {
	ws := []v1alpha1.TimeWindow{
		{Schedule: "0 10 * * *", Duration: dur(time.Hour)},                      // 10:00-11:00
		{Start: at(now.Add(20 * time.Minute)), End: at(now.Add(2 * time.Hour))}, // 10:50-12:30, chained
	}
	got, ok := End(ws, now)
	if !ok || !got.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("End = %v %v", got, ok)
	}
	if _, ok := End(ws[1:], now); ok {
		t.Fatal("not open now: no end")
	}
}

func TestFreeze(t *testing.T) {
	policy := []v1alpha1.TimeWindow{{Schedule: "0 22 * * *", Duration: dur(time.Hour)}}
	if got := (Freeze{}).Merge(policy); len(got) != 1 {
		t.Fatalf("no cluster freeze must keep the policy windows: %v", got)
	}
	f := Freeze{Windows: []v1alpha1.TimeWindow{{Start: at(now.Add(-time.Hour)), End: at(now.Add(time.Hour))}}}
	if open, _ := Open(f.Merge(policy), now); !open {
		t.Fatal("cluster window must freeze")
	}
	if open, _ := Open((Freeze{Now: true}).Merge(nil), now); !open {
		t.Fatal("now must freeze")
	}
	if len(policy) != 1 {
		t.Fatal("Merge modified its input")
	}

	dir := t.TempDir()
	if f, err := LoadFreeze(filepath.Join(dir, "missing.yaml")); err != nil || f.Now {
		t.Fatalf("missing file: %+v %v", f, err)
	}
	p := filepath.Join(dir, "freeze.yaml")
	_ = os.WriteFile(p, []byte("now: true\nwindows:\n- schedule: \"0 18 * * 5\"\n  duration: 62h\n  timeZone: Asia/Jerusalem\n"), 0o600)
	f, err := LoadFreeze(p)
	if err != nil || !f.Now || len(f.Windows) != 1 || f.Windows[0].Duration.Duration != 62*time.Hour {
		t.Fatalf("load: %+v %v", f, err)
	}
	_ = os.WriteFile(p, []byte("windows:\n- schedule: \"bad\"\n  duration: 1h\n"), 0o600)
	if _, err := LoadFreeze(p); err == nil {
		t.Fatal("invalid window must fail")
	}
	_ = os.WriteFile(p, []byte("nwo: true\n"), 0o600)
	if _, err := LoadFreeze(p); err == nil {
		t.Fatal("unknown field must fail")
	}
}
