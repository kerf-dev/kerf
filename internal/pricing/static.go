package pricing

import (
	"context"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// StaticFile is the format of the static price file (see dev/prices.yaml).
type StaticFile struct {
	// InstanceTypes holds the shape of every instance type that has a price.
	InstanceTypes map[string]StaticShape `yaml:"instanceTypes"`
	// Regions maps region -> instance type -> prices.
	Regions map[string]map[string]StaticPrice `yaml:"regions"`
}

// StaticShape is an instance type's vCPU count and memory in GiB.
type StaticShape struct {
	VCPU      float64 `yaml:"vcpu"`
	MemoryGiB float64 `yaml:"memoryGiB"`
}

// StaticPrice holds USD/hour prices for one instance type in one region.
type StaticPrice struct {
	OnDemand float64 `yaml:"onDemand"`
	// Spot is the price used for a zone not listed in SpotByZone (0 = no spot price).
	Spot       float64            `yaml:"spot"`
	SpotByZone map[string]float64 `yaml:"spotByZone"`
}

// Static serves prices from a StaticFile. It is used locally and in tests.
type Static struct{ file StaticFile }

// LoadStatic reads and validates a static price file.
func LoadStatic(path string) (*Static, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseStatic(b)
}

// ParseStatic parses and validates a static price file.
func ParseStatic(b []byte) (*Static, error) {
	var f StaticFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse static prices: %w", err)
	}
	for name, s := range f.InstanceTypes {
		if s.VCPU <= 0 || s.MemoryGiB <= 0 {
			return nil, fmt.Errorf("instance type %s: vcpu and memoryGiB must be > 0", name)
		}
	}
	for region, types := range f.Regions {
		for name, p := range types {
			if _, ok := f.InstanceTypes[name]; !ok {
				return nil, fmt.Errorf("%s/%s: instance type has no entry in instanceTypes", region, name)
			}
			if p.OnDemand < 0 || p.Spot < 0 {
				return nil, fmt.Errorf("%s/%s: prices must be >= 0", region, name)
			}
			for zone, v := range p.SpotByZone {
				if v <= 0 {
					return nil, fmt.Errorf("%s/%s: spotByZone[%s] must be > 0", region, name, zone)
				}
			}
		}
	}
	return &Static{file: f}, nil
}

func (s *Static) Name() string { return "static" }

func (s *Static) Price(_ context.Context, k Key) (Price, error) {
	p, ok := s.file.Regions[k.Region][k.InstanceType]
	if !ok {
		return Price{}, fmt.Errorf("%s: %w", k, ErrNotFound)
	}
	shape := s.file.InstanceTypes[k.InstanceType]
	var usd float64
	switch k.CapacityType {
	case Spot:
		usd = p.Spot
		if z, ok := p.SpotByZone[k.Zone]; ok {
			usd = z
		}
	default:
		usd = p.OnDemand
	}
	if usd <= 0 {
		return Price{}, fmt.Errorf("%s: %w", k, ErrNotFound)
	}
	return Price{HourlyUSD: usd, Shape: Shape(shape)}, nil
}
