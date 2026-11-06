package pricing

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Effective rates from the AWS Cost and Usage Report (CUR 2.0, exported by AWS Data Exports as
// Parquet in S3). The rate for an instance type, region and capacity type is
//
//	rate = sum(amortised cost) / sum(usage hours)
//
// over the EC2 Linux instance usage of the window (30 days by default), where the amortised cost of a
// line item follows AWS's "amortized cost" by line_item_line_item_type:
//
//	Usage                    line_item_unblended_cost        (on-demand and spot hours)
//	SavingsPlanCoveredUsage  savings_plan_savings_plan_effective_cost
//	DiscountedUsage          reservation_effective_cost      (hours covered by a Reserved Instance)
//	RIFee                    reservation_unused_amortized_upfront_fee_for_billing_period
//	                         + reservation_unused_recurring_fee   (unused RI hours; no usage hours)
//
// Only the hours of Usage, SavingsPlanCoveredUsage and DiscountedUsage lines count. Line items of other
// types (SavingsPlanNegation, SavingsPlanUpfrontFee, SavingsPlanRecurringFee, Fee, Credit, Tax,
// discounts) are left out: the effective-cost columns already contain what they add up to, except
// unused Savings Plan commitment, which belongs to no instance type. docs/pricing.md explains it.
//
// Only aggregates are kept: the readers return one CURGroup per (region, instance type, capacity
// type, line item type), never line items.

// CUR 2.0 line item types used for rates.
const (
	LineItemUsage                   = "Usage"
	LineItemSavingsPlanCoveredUsage = "SavingsPlanCoveredUsage"
	LineItemDiscountedUsage         = "DiscountedUsage"
	LineItemRIFee                   = "RIFee"
)

// curLineItemTypes are the line item types the readers aggregate.
var curLineItemTypes = []string{LineItemUsage, LineItemSavingsPlanCoveredUsage, LineItemDiscountedUsage, LineItemRIFee}

// CURGroup is the sum of the relevant CUR columns over the line items of one
// (region, instance type, capacity type, line item type).
type CURGroup struct {
	Region       string
	InstanceType string
	CapacityType CapacityType
	LineItemType string
	// First usage start and last usage end of the group's line items.
	Start, End time.Time

	UsageAmount                   float64 // line_item_usage_amount (hours)
	UnblendedCost                 float64 // line_item_unblended_cost
	SavingsPlanEffectiveCost      float64 // savings_plan_savings_plan_effective_cost
	ReservationEffectiveCost      float64 // reservation_effective_cost
	ReservationUnusedAmortizedFee float64 // reservation_unused_amortized_upfront_fee_for_billing_period
	ReservationUnusedRecurringFee float64 // reservation_unused_recurring_fee
}

// cost returns the group's amortised cost and its usage hours.
func (g CURGroup) cost() (usd, hours float64, ok bool) {
	switch g.LineItemType {
	case LineItemUsage:
		return g.UnblendedCost, g.UsageAmount, true
	case LineItemSavingsPlanCoveredUsage:
		return g.SavingsPlanEffectiveCost, g.UsageAmount, true
	case LineItemDiscountedUsage:
		return g.ReservationEffectiveCost, g.UsageAmount, true
	case LineItemRIFee:
		return g.ReservationUnusedAmortizedFee + g.ReservationUnusedRecurringFee, 0, true
	}
	return 0, 0, false
}

// CURReader reads the aggregates of EC2 Linux instance usage whose usage starts in [from, to).
type CURReader interface {
	// Name is "athena" or "s3", for logs.
	Name() string
	Groups(ctx context.Context, from, to time.Time) ([]CURGroup, error)
}

// CURRate is the effective rate of one key and what it was computed from.
type CURRate struct {
	HourlyUSD float64
	CostUSD   float64
	Hours     float64
}

// CURRates are the effective rates of one report read.
type CURRates struct {
	// Rates is keyed by region, instance type and capacity type (no zone).
	Rates map[Key]CURRate
	// Start and End bound the usage the rates were computed from.
	Start, End time.Time
}

// ComputeCURRates turns the readers' aggregates into effective hourly rates. A key gets a rate only
// when it has usage hours and a positive cost; the others fall back to list prices.
func ComputeCURRates(groups []CURGroup) CURRates {
	type acc struct{ usd, hours float64 }
	sums := map[Key]*acc{}
	out := CURRates{Rates: map[Key]CURRate{}}
	for _, g := range groups {
		usd, hours, ok := g.cost()
		if !ok || g.Region == "" || g.InstanceType == "" {
			continue
		}
		k := Key{Region: g.Region, InstanceType: g.InstanceType, CapacityType: g.CapacityType}
		a := sums[k]
		if a == nil {
			a = &acc{}
			sums[k] = a
		}
		a.usd += usd
		a.hours += hours
		if hours > 0 { // the period is the one of usage, not of RI fees
			if !g.Start.IsZero() && (out.Start.IsZero() || g.Start.Before(out.Start)) {
				out.Start = g.Start
			}
			if g.End.After(out.End) {
				out.End = g.End
			}
		}
	}
	for k, a := range sums {
		if a.hours > 0 && a.usd > 0 {
			out.Rates[k] = CURRate{HourlyUSD: a.usd / a.hours, CostUSD: a.usd, Hours: a.hours}
		}
	}
	return out
}

// curCapacityType classifies a usage line: spot when its pricing term is Spot or its usage type is a
// SpotUsage one (e.g. EUW1-SpotUsage:m6i.xlarge), else on-demand (which includes hours covered by
// Savings Plans and Reserved Instances).
func curCapacityType(pricingTerm, usageType string) CapacityType {
	if strings.EqualFold(pricingTerm, "Spot") || strings.Contains(usageType, "SpotUsage") {
		return Spot
	}
	return OnDemand
}

// curInstanceUsage reports whether a line item is EC2 Linux instance usage (or its RI fee): product
// AmazonEC2, operation RunInstances (Linux/UNIX, shared or default tenancy) and a BoxUsage, SpotUsage
// or HeavyUsage (RI) usage type.
func curInstanceUsage(productCode, operation, usageType, instanceType string) bool {
	if productCode != "AmazonEC2" || operation != "RunInstances" || instanceType == "" {
		return false
	}
	for _, s := range []string{"BoxUsage", "SpotUsage", "HeavyUsage"} {
		if strings.Contains(usageType, s) {
			return true
		}
	}
	return false
}

func isCURLineItemType(t string) bool {
	for _, x := range curLineItemTypes {
		if t == x {
			return true
		}
	}
	return false
}

// curGroupKey identifies a CURGroup while aggregating.
type curGroupKey struct {
	region, instanceType string
	capacityType         CapacityType
	lineItemType         string
}

// curAggregator sums line items into CURGroups (the S3 reader, and fakes in tests).
type curAggregator struct {
	groups map[curGroupKey]*CURGroup
}

// CURLineItem is the subset of a CUR 2.0 line item that rates are computed from.
type CURLineItem struct {
	ProductCode, Operation, UsageType, LineItemType, PricingTerm string
	InstanceType, Region                                         string
	UsageStart, UsageEnd                                         time.Time
	UsageAmount, UnblendedCost, SavingsPlanEffectiveCost         float64
	ReservationEffectiveCost, ReservationUnusedAmortizedFee      float64
	ReservationUnusedRecurringFee                                float64
}

// add sums a line item whose usage starts in [from, to); others are ignored.
func (a *curAggregator) add(li CURLineItem, from, to time.Time) {
	if !isCURLineItemType(li.LineItemType) ||
		!curInstanceUsage(li.ProductCode, li.Operation, li.UsageType, li.InstanceType) ||
		li.UsageStart.Before(from) || !li.UsageStart.Before(to) {
		return
	}
	k := curGroupKey{li.Region, li.InstanceType, curCapacityType(li.PricingTerm, li.UsageType), li.LineItemType}
	if a.groups == nil {
		a.groups = map[curGroupKey]*CURGroup{}
	}
	g := a.groups[k]
	if g == nil {
		g = &CURGroup{Region: k.region, InstanceType: k.instanceType, CapacityType: k.capacityType, LineItemType: k.lineItemType, Start: li.UsageStart, End: li.UsageEnd}
		a.groups[k] = g
	}
	if li.UsageStart.Before(g.Start) {
		g.Start = li.UsageStart
	}
	if li.UsageEnd.After(g.End) {
		g.End = li.UsageEnd
	}
	g.UsageAmount += li.UsageAmount
	g.UnblendedCost += li.UnblendedCost
	g.SavingsPlanEffectiveCost += li.SavingsPlanEffectiveCost
	g.ReservationEffectiveCost += li.ReservationEffectiveCost
	g.ReservationUnusedAmortizedFee += li.ReservationUnusedAmortizedFee
	g.ReservationUnusedRecurringFee += li.ReservationUnusedRecurringFee
}

// result returns the groups in a stable order.
func (a *curAggregator) result() []CURGroup {
	out := make([]CURGroup, 0, len(a.groups))
	for _, g := range a.groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i], out[j]
		return fmt.Sprint(x.Region, x.InstanceType, x.CapacityType, x.LineItemType) < fmt.Sprint(y.Region, y.InstanceType, y.CapacityType, y.LineItemType)
	})
	return out
}
