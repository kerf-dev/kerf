// Package pricing prices Kubernetes nodes and splits each node's hourly price into per-vCPU-hour and
// per-GiB-hour rates (WO-03, D-008). Nodes are discovered from spoke metrics in the hub's
// VictoriaMetrics, priced by a Provider (AWS or a static file) and exported as the
// ro_*_hourly_cost_usd series defined in docs/contracts/metrics.md.
package pricing

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// CapacityType follows Karpenter's karpenter.sh/capacity-type values.
type CapacityType string

const (
	OnDemand CapacityType = "on-demand"
	Spot     CapacityType = "spot"
)

// ParseCapacityType accepts Karpenter values (on-demand, spot) and EKS managed node group values
// (ON_DEMAND, SPOT). An empty value means on-demand.
func ParseCapacityType(s string) (CapacityType, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", "-")) {
	case "", "on-demand", "ondemand":
		return OnDemand, nil
	case "spot":
		return Spot, nil
	}
	return "", fmt.Errorf("unknown capacity type %q", s)
}

// Key identifies the price of one instance. Zone matters only for spot prices.
type Key struct {
	Region       string
	Zone         string
	InstanceType string
	CapacityType CapacityType
}

func (k Key) String() string {
	s := k.Region + "/" + k.InstanceType + "/" + string(k.CapacityType)
	if k.Zone != "" {
		s += "/" + k.Zone
	}
	return s
}

// normalized drops the zone for on-demand keys, whose price does not depend on it.
func (k Key) normalized() Key {
	if k.CapacityType != Spot {
		k.Zone = ""
	}
	return k
}

// Shape is an instance type's capacity as sold (not the node's allocatable).
type Shape struct {
	VCPU      float64
	MemoryGiB float64
}

// Price is the hourly price of one instance.
type Price struct {
	HourlyUSD float64
	Shape     Shape
}

// ErrNotFound means the provider has no price for the key. It is not a transient error.
var ErrNotFound = errors.New("price not found")

// Provider prices one instance (region, instance type, capacity type).
type Provider interface {
	// Name is the value of the `provider` label: "aws" or "static".
	Name() string
	Price(ctx context.Context, k Key) (Price, error)
}
