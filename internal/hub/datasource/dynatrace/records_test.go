package dynatrace

import (
	"encoding/json"
	"testing"
	"time"
)

func TestScale(t *testing.T) {
	for _, tc := range []struct {
		kind   unitKind
		unit   string
		factor float64
		known  bool
	}{
		{unitCPU, "MilliCores", 1e-3, true},
		{unitCPU, "millicore", 1e-3, true},
		{unitCPU, "Cores", 1, true},
		{unitCPU, "NanoCores", 1e-9, true},
		{unitCPU, "", 1e-3, false},
		{unitCPU, "Furlongs", 1e-3, false},
		{unitBytes, "Byte", 1, true},
		{unitBytes, "MebiByte", 1 << 20, true},
		{unitBytes, "KiloByte", 1e3, true},
		{unitBytes, "", 1, false},
		{unitRatio, "Percent", 0.01, true},
		{unitRatio, "%", 0.01, true},
		{unitRatio, "Ratio", 1, true},
		{unitRatio, "", 0.01, false},
	} {
		f, known := scale(tc.kind, tc.unit)
		if f != tc.factor || known != tc.known {
			t.Errorf("scale(%v, %q) = %v, %v; want %v, %v", tc.kind, tc.unit, f, known, tc.factor, tc.known)
		}
	}
}

func TestSeries(t *testing.T) {
	var rec map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"timeframe":{"start":"2026-11-09T11:00:00.000000000Z"},"interval":"60000000000","v":[1,null,"2.5","NaN"]}`), &rec); err != nil {
		t.Fatal(err)
	}
	s, err := series(rec, "v", 2)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 9, 11, 0, 0, 0, time.UTC)
	if len(s) != 2 || !s[0].T.Equal(t0.Add(time.Minute)) || s[0].V != 2 || !s[1].T.Equal(t0.Add(3*time.Minute)) || s[1].V != 5 {
		t.Errorf("series = %v (bucket end stamps, nulls and NaN dropped)", s)
	}
}

func TestChunkAndInterval(t *testing.T) {
	end := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	c := chunk(end.Add(-28*24*time.Hour), end, 5*time.Minute)
	if len(c) != 6 || !c[5][1].Equal(end) || c[1][0] != c[0][1] {
		t.Errorf("28 days at 5m: %d chunks %v", len(c), c)
	}
	for _, tc := range []struct{ d, step, want time.Duration }{
		{7 * 24 * time.Hour, time.Hour, time.Hour},
		{25 * time.Hour, 5 * time.Minute, 5 * time.Minute},
		{90 * 24 * time.Hour, time.Hour, 2 * time.Hour},
	} {
		if got := binInterval(tc.d, tc.step); got != tc.want {
			t.Errorf("binInterval(%v, %v) = %v, want %v", tc.d, tc.step, got, tc.want)
		}
	}
}

func TestLabelsOf(t *testing.T) {
	for _, tc := range []struct{ raw, key, want string }{
		{`{"karpenter.sh/capacity-type":"spot"}`, "karpenter.sh/capacity-type", "spot"},
		{`["eks.amazonaws.com/capacityType=ON_DEMAND"]`, "eks.amazonaws.com/capacityType", "ON_DEMAND"},
		{`["topology.kubernetes.io/zone:eu-west-1a"]`, "topology.kubernetes.io/zone", "eu-west-1a"},
		{`"{\"node.kubernetes.io/instance-type\":\"m6i.large\"}"`, "node.kubernetes.io/instance-type", "m6i.large"},
		{`null`, "x", ""},
	} {
		m := map[string]string{}
		labelsOf(json.RawMessage(tc.raw), m)
		if m[tc.key] != tc.want {
			t.Errorf("labelsOf(%s)[%s] = %q, want %q", tc.raw, tc.key, m[tc.key], tc.want)
		}
	}
}

func TestDQLString(t *testing.T) {
	if got := dqlString(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("dqlString = %s", got)
	}
}
