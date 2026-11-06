// Package pricing prices Kubernetes nodes and splits each node's hourly price into per-vCPU-hour and
// per-GiB-hour rates. Nodes are discovered from spoke metrics in the hub's VictoriaMetrics, priced by
// a Provider (the AWS price list, a static file, and optionally the effective rates of the AWS Cost
// and Usage Report on top) and exported as the kerf_*_hourly_cost_usd series defined in
// docs/contracts/metrics.md.
package pricing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
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

// Rate sources: the `source` label of the cost series.
const (
	// SourceCUR is the effective rate paid over a past period, from the AWS Cost and Usage Report
	// (Savings Plans and Reserved Instances amortised).
	SourceCUR = "cur"
	// SourceList is the on-demand list price from the AWS Price List API.
	SourceList = "list"
	// SourceSpot is the current spot price from the EC2 spot price history.
	SourceSpot = "spot"
	// SourceStatic is a price from the static price file.
	SourceStatic = "static"
)

// Price is the hourly price of one instance.
type Price struct {
	HourlyUSD float64
	Shape     Shape
	// Source is one of the Source* constants.
	Source string
	// PeriodStart and PeriodEnd bound the usage a SourceCUR rate was computed from; zero for
	// current prices.
	PeriodStart, PeriodEnd time.Time
	// GPUs is the number of GPUs of the instance type; 0 for instances without GPUs.
	GPUs float64
}

// ErrNotFound means the provider has no price for the key. It is not a transient error.
var ErrNotFound = errors.New("price not found")

// Provider prices one instance (region, instance type, capacity type).
type Provider interface {
	// Name is the value of the `provider` label: "aws" or "static".
	Name() string
	Price(ctx context.Context, k Key) (Price, error)
}
