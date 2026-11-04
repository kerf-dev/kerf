package pricing

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDevPricesFile(t *testing.T) {
	s, err := LoadStatic("../../dev/prices.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// Every instance type used by the real spokes and synth-a/synth-b must be priced, for both capacity types.
	for _, it := range []string{"m6i.xlarge", "c6a.2xlarge"} {
		for _, ct := range []CapacityType{OnDemand, Spot} {
			p, err := s.Price(context.Background(), Key{Region: "eu-west-1", Zone: "eu-west-1a", InstanceType: it, CapacityType: ct})
			if err != nil || p.HourlyUSD <= 0 {
				t.Errorf("%s %s: %+v %v", it, ct, p, err)
			}
		}
	}
	// Spot must be cheaper than on-demand everywhere, and every c/m/r family must derive a sane weight.
	sp := &Splitter{Provider: s}
	for region, types := range s.file.Regions {
		for it, p := range types {
			if p.Spot >= p.OnDemand {
				t.Errorf("%s/%s: spot %v >= on-demand %v", region, it, p.Spot, p.OnDemand)
			}
			if w, src := sp.Weight(context.Background(), region, it); src != WeightDerived || w < 4 || w > 20 {
				t.Errorf("%s/%s: weight %v (%s)", region, it, w, src)
			}
		}
	}
}

func TestStaticPrice(t *testing.T) {
	s, err := ParseStatic([]byte(`
instanceTypes:
  m6i.xlarge: {vcpu: 4, memoryGiB: 16}
  t3.micro: {vcpu: 2, memoryGiB: 1}
regions:
  eu-west-1:
    m6i.xlarge: {onDemand: 0.214, spot: 0.08, spotByZone: {eu-west-1a: 0.07}}
    t3.micro: {onDemand: 0.0114}
`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "static" {
		t.Fatalf("name %q", s.Name())
	}
	tests := []struct {
		name string
		key  Key
		want float64 // 0 = ErrNotFound
	}{
		{"on-demand", Key{"eu-west-1", "eu-west-1a", "m6i.xlarge", OnDemand}, 0.214},
		{"spot by zone", Key{"eu-west-1", "eu-west-1a", "m6i.xlarge", Spot}, 0.07},
		{"spot default", Key{"eu-west-1", "eu-west-1c", "m6i.xlarge", Spot}, 0.08},
		{"no spot price", Key{"eu-west-1", "", "t3.micro", Spot}, 0},
		{"unknown type", Key{"eu-west-1", "", "m5.large", OnDemand}, 0},
		{"unknown region", Key{"us-east-1", "", "m6i.xlarge", OnDemand}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := s.Price(context.Background(), tt.key)
			if tt.want == 0 {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
				return
			}
			if err != nil || p.HourlyUSD != tt.want {
				t.Fatalf("got %+v, %v; want %v", p, err, tt.want)
			}
		})
	}
}

func TestParseStaticErrors(t *testing.T) {
	tests := []struct{ name, doc, errText string }{
		{"bad yaml", "instanceTypes: [", "parse"},
		{"bad shape", "instanceTypes: {m6i.xlarge: {vcpu: 0, memoryGiB: 16}}", "must be > 0"},
		{"unknown type", "regions: {eu-west-1: {m6i.xlarge: {onDemand: 1}}}", "no entry in instanceTypes"},
		{"negative", "instanceTypes: {a.b: {vcpu: 1, memoryGiB: 1}}\nregions: {r: {a.b: {onDemand: -1}}}", ">= 0"},
		{"bad zone", "instanceTypes: {a.b: {vcpu: 1, memoryGiB: 1}}\nregions: {r: {a.b: {spotByZone: {z: 0}}}}", "spotByZone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseStatic([]byte(tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.errText) {
				t.Fatalf("err = %v, want containing %q", err, tt.errText)
			}
		})
	}
	if _, err := LoadStatic("does-not-exist.yaml"); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestParseCapacityType(t *testing.T) {
	tests := []struct {
		in   string
		want CapacityType
		ok   bool
	}{
		{"", OnDemand, true}, {"on-demand", OnDemand, true}, {"ON_DEMAND", OnDemand, true},
		{"spot", Spot, true}, {"SPOT", Spot, true}, {"reserved", "", false},
	}
	for _, tt := range tests {
		got, err := ParseCapacityType(tt.in)
		if got != tt.want || (err == nil) != tt.ok {
			t.Errorf("ParseCapacityType(%q) = %q, %v", tt.in, got, err)
		}
	}
	if got := (Key{"r", "z", "t", Spot}).String(); got != "r/t/spot/z" {
		t.Errorf("Key.String() = %q", got)
	}
}
