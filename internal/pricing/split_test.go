package pricing

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		name               string
		usd, cores, gib, w float64
		wantCPU, wantMem   float64
		wantErr            bool
	}{
		{"weight 9, m6i.xlarge allocatable", 0.214, 4, 16, 9, 0.214 * 9 / 52, 0.214 / 52, false},
		{"weight 1", 1, 1, 1, 1, 0.5, 0.5, false},
		{"zero cores", 1, 0, 1, 9, 0, 0, true},
		{"zero memory", 1, 1, 0, 9, 0, 0, true},
		{"zero weight", 1, 1, 1, 0, 0, 0, true},
		{"NaN weight", 1, 1, 1, math.NaN(), 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cpu, mem, err := Split(tt.usd, tt.cores, tt.gib, tt.w)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr {
				return
			}
			if math.Abs(cpu-tt.wantCPU) > 1e-12 || math.Abs(mem-tt.wantMem) > 1e-12 {
				t.Fatalf("got %v/%v, want %v/%v", cpu, mem, tt.wantCPU, tt.wantMem)
			}
			// Contract invariant.
			if got := cpu*tt.cores + mem*tt.gib; math.Abs(got-tt.usd) > 1e-12 {
				t.Fatalf("cpu*cores + mem*gib = %v, want %v", got, tt.usd)
			}
		})
	}
}

func TestSiblings(t *testing.T) {
	tests := map[string][]string{
		"m6i.xlarge":   {"c6i.xlarge", "m6i.xlarge", "r6i.xlarge"},
		"c6a.2xlarge":  {"c6a.2xlarge", "m6a.2xlarge", "r6a.2xlarge"},
		"r7gd.metal":   {"c7gd.metal", "m7gd.metal", "r7gd.metal"},
		"t3.medium":    nil,
		"x2idn.16xl":   nil,
		"not-a-type":   nil,
		"m5.large.foo": nil,
	}
	for in, want := range tests {
		got := siblings(in)
		if len(got) != len(want) {
			t.Errorf("siblings(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("siblings(%q) = %v, want %v", in, got, want)
			}
		}
	}
}

type fakeProvider struct {
	prices map[Key]Price
	errs   map[Key]error
	calls  map[Key]int
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Price(_ context.Context, k Key) (Price, error) {
	if f.calls == nil {
		f.calls = map[Key]int{}
	}
	f.calls[k]++
	if err, ok := f.errs[k]; ok {
		return Price{}, err
	}
	if p, ok := f.prices[k]; ok {
		return p, nil
	}
	return Price{}, ErrNotFound
}

func od(it string) Key { return Key{Region: "eu-west-1", InstanceType: it, CapacityType: OnDemand} }

func TestSplitterWeight(t *testing.T) {
	// Prices built from a = 0.036/vCPU, b = 0.004/GiB, i.e. a weight of exactly 9.
	exact := map[Key]Price{
		od("c6i.xlarge"): {0.036*4 + 0.004*8, Shape{4, 8}},
		od("m6i.xlarge"): {0.036*4 + 0.004*16, Shape{4, 16}},
		od("r6i.xlarge"): {0.036*4 + 0.004*32, Shape{4, 32}},
		// Only the m class is known for 6a: fallback.
		od("m6a.xlarge"): {0.19, Shape{4, 16}},
		// Memory is priced negatively: fit rejected.
		od("c5.xlarge"): {0.30, Shape{4, 8}},
		od("m5.xlarge"): {0.20, Shape{4, 16}},
	}
	tests := []struct {
		name, instanceType string
		fixed, fallback    float64
		want               float64
		wantSource         string
	}{
		{"derived", "m6i.xlarge", 0, 0, 9, WeightDerived},
		{"derived for sibling", "r6i.xlarge", 0, 0, 9, WeightDerived},
		{"one ratio only", "m6a.xlarge", 0, 0, DefaultCPUWeight, WeightFallback},
		{"non-positive fit", "m5.xlarge", 0, 0, DefaultCPUWeight, WeightFallback},
		{"custom fallback", "t3.medium", 0, 7, 7, WeightFallback},
		{"fixed", "m6i.xlarge", 4, 0, 4, WeightFixed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Splitter{Provider: &fakeProvider{prices: exact}, Fixed: tt.fixed, Fallback: tt.fallback}
			w, src := s.Weight(context.Background(), "eu-west-1", tt.instanceType)
			if math.Abs(w-tt.want) > 1e-9 || src != tt.wantSource {
				t.Fatalf("got %v (%s), want %v (%s)", w, src, tt.want, tt.wantSource)
			}
		})
	}
}

func TestSplitterWeightCaching(t *testing.T) {
	transient := errors.New("throttled")
	f := &fakeProvider{
		prices: map[Key]Price{
			od("c6i.xlarge"): {0.192, Shape{4, 8}},
			od("m6i.xlarge"): {0.214, Shape{4, 16}},
			od("r6i.xlarge"): {0.282, Shape{4, 32}},
		},
		errs: map[Key]error{od("r6i.xlarge"): transient},
	}
	s := &Splitter{Provider: f}
	ctx := context.Background()
	// c and m still give two ratios, but the transient failure means the result is not cached.
	if _, src := s.Weight(ctx, "eu-west-1", "m6i.xlarge"); src != WeightDerived {
		t.Fatalf("source %s", src)
	}
	delete(f.errs, od("r6i.xlarge"))
	w1, _ := s.Weight(ctx, "eu-west-1", "m6i.xlarge")
	w2, _ := s.Weight(ctx, "eu-west-1", "c6i.xlarge")
	if w1 != w2 {
		t.Fatalf("siblings got different weights %v %v", w1, w2)
	}
	if n := f.calls[od("r6i.xlarge")]; n != 2 {
		t.Fatalf("r6i.xlarge fetched %d times, want 2 (retry after transient error, then cached)", n)
	}
}

func TestFitWeightDegenerate(t *testing.T) {
	if _, ok := fitWeight(nil); ok {
		t.Fatal("empty fit must fail")
	}
	// Invalid points are ignored.
	if _, ok := fitWeight([]Price{{0, Shape{4, 8}}, {1, Shape{0, 16}}, {1, Shape{4, 16}}}); ok {
		t.Fatal("one valid point must fail")
	}
}
