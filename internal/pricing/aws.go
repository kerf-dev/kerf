package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	pricingtypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"
)

// PricingAPI is the part of the AWS Pricing API client used here.
type PricingAPI interface {
	GetProducts(ctx context.Context, in *pricing.GetProductsInput, opts ...func(*pricing.Options)) (*pricing.GetProductsOutput, error)
}

// SpotAPI is the part of the EC2 client used here.
type SpotAPI interface {
	DescribeSpotPriceHistory(ctx context.Context, in *ec2.DescribeSpotPriceHistoryInput, opts ...func(*ec2.Options)) (*ec2.DescribeSpotPriceHistoryOutput, error)
}

// DefaultPricingRegion is where the AWS Pricing API is served (it also exists in eu-central-1 and
// ap-south-1); prices for every region are available from it.
const DefaultPricingRegion = "us-east-1"

// AWS prices on-demand instances from the AWS Pricing API (Linux, shared tenancy, no pre-installed
// software) and spot instances from EC2 spot price history. Wrap it in Cached: every call goes to AWS.
type AWS struct {
	Pricing PricingAPI
	// Spot returns the EC2 client for a region (spot prices are regional endpoints).
	Spot func(region string) SpotAPI
	Now  func() time.Time

	mu     sync.Mutex
	shapes map[string]Shape // region/instanceType -> shape, never changes
}

// NewAWS builds the provider from an AWS SDK config (credentials from the default chain, e.g. IRSA).
func NewAWS(cfg aws.Config, pricingRegion string) *AWS {
	if pricingRegion == "" {
		pricingRegion = DefaultPricingRegion
	}
	pc := pricing.NewFromConfig(cfg, func(o *pricing.Options) { o.Region = pricingRegion })
	var mu sync.Mutex
	clients := map[string]*ec2.Client{}
	return &AWS{
		Pricing: pc,
		Spot: func(region string) SpotAPI {
			mu.Lock()
			defer mu.Unlock()
			c, ok := clients[region]
			if !ok {
				c = ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.Region = region })
				clients[region] = c
			}
			return c
		},
	}
}

func (a *AWS) Name() string { return "aws" }

func (a *AWS) Price(ctx context.Context, k Key) (Price, error) {
	if k.Region == "" || k.InstanceType == "" {
		return Price{}, fmt.Errorf("%s: region and instance type are required: %w", k, ErrNotFound)
	}
	od, err := a.onDemand(ctx, k.Region, k.InstanceType)
	if err != nil {
		return Price{}, err
	}
	if k.CapacityType != Spot {
		return od, nil
	}
	usd, err := a.spot(ctx, k)
	if err != nil {
		return Price{}, err
	}
	return Price{HourlyUSD: usd, Shape: od.Shape}, nil
}

func (a *AWS) onDemand(ctx context.Context, region, instanceType string) (Price, error) {
	in := &pricing.GetProductsInput{
		ServiceCode:   aws.String("AmazonEC2"),
		FormatVersion: aws.String("aws_v1"),
		Filters: []pricingtypes.Filter{
			termMatch("instanceType", instanceType),
			termMatch("regionCode", region),
			termMatch("operatingSystem", "Linux"),
			termMatch("tenancy", "Shared"),
			termMatch("preInstalledSw", "NA"),
			termMatch("capacitystatus", "Used"),
			termMatch("licenseModel", "No License required"),
			termMatch("marketoption", "OnDemand"),
		},
	}
	key := Key{Region: region, InstanceType: instanceType, CapacityType: OnDemand}
	var found []Price
	for {
		out, err := a.Pricing.GetProducts(ctx, in)
		if err != nil {
			return Price{}, fmt.Errorf("aws pricing GetProducts %s: %w", key, err)
		}
		for _, doc := range out.PriceList {
			p, err := parsePriceListItem(doc, instanceType)
			if err != nil {
				return Price{}, fmt.Errorf("aws pricing %s: %w", key, err)
			}
			if p.HourlyUSD > 0 {
				found = append(found, p)
			}
		}
		if out.NextToken == nil || *out.NextToken == "" {
			break
		}
		in.NextToken = out.NextToken
	}
	if len(found) == 0 {
		return Price{}, fmt.Errorf("%s: %w", key, ErrNotFound)
	}
	// The filters select one product; if AWS ever returns more, take the cheapest so the
	// estimate stays conservative.
	best := found[0]
	for _, p := range found[1:] {
		if p.HourlyUSD < best.HourlyUSD {
			best = p
		}
	}
	a.mu.Lock()
	if a.shapes == nil {
		a.shapes = map[string]Shape{}
	}
	a.shapes[region+"/"+instanceType] = best.Shape
	a.mu.Unlock()
	return best, nil
}

func termMatch(field, value string) pricingtypes.Filter {
	return pricingtypes.Filter{Type: pricingtypes.FilterTypeTermMatch, Field: aws.String(field), Value: aws.String(value)}
}

// priceListItem is the subset of an AWS price list product document that is used.
type priceListItem struct {
	Product struct {
		Attributes struct {
			InstanceType string `json:"instanceType"`
			VCPU         string `json:"vcpu"`
			Memory       string `json:"memory"`
		} `json:"attributes"`
	} `json:"product"`
	Terms struct {
		OnDemand map[string]struct {
			PriceDimensions map[string]struct {
				Unit         string            `json:"unit"`
				PricePerUnit map[string]string `json:"pricePerUnit"`
			} `json:"priceDimensions"`
		} `json:"OnDemand"`
	} `json:"terms"`
}

func parsePriceListItem(doc, instanceType string) (Price, error) {
	var it priceListItem
	if err := json.Unmarshal([]byte(doc), &it); err != nil {
		return Price{}, fmt.Errorf("decode price list item: %w", err)
	}
	attrs := it.Product.Attributes
	if attrs.InstanceType != instanceType {
		return Price{}, fmt.Errorf("price list item is for %q, want %q", attrs.InstanceType, instanceType)
	}
	vcpu, err := strconv.ParseFloat(attrs.VCPU, 64)
	if err != nil {
		return Price{}, fmt.Errorf("vcpu %q: %w", attrs.VCPU, err)
	}
	mem, err := parseMemoryGiB(attrs.Memory)
	if err != nil {
		return Price{}, err
	}
	var usd float64
	for _, term := range it.Terms.OnDemand {
		for _, dim := range term.PriceDimensions {
			if dim.Unit != "Hrs" {
				continue
			}
			v, err := strconv.ParseFloat(dim.PricePerUnit["USD"], 64)
			if err != nil {
				return Price{}, fmt.Errorf("price %q: %w", dim.PricePerUnit["USD"], err)
			}
			if v > usd {
				usd = v
			}
		}
	}
	return Price{HourlyUSD: usd, Shape: Shape{VCPU: vcpu, MemoryGiB: mem}}, nil
}

// parseMemoryGiB parses the price list's memory attribute, e.g. "16 GiB" or "0.5 GiB".
func parseMemoryGiB(s string) (float64, error) {
	f := strings.Fields(strings.ReplaceAll(s, ",", ""))
	if len(f) != 2 || f[1] != "GiB" {
		return 0, fmt.Errorf("memory %q: want \"<n> GiB\"", s)
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("memory %q: invalid size", s)
	}
	return v, nil
}

// spot returns the current spot price: the zone's price if the key has a zone that AWS reports, else
// the mean over the region's zones.
func (a *AWS) spot(ctx context.Context, k Key) (float64, error) {
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	start := now()
	in := &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       []ec2types.InstanceType{ec2types.InstanceType(k.InstanceType)},
		ProductDescriptions: []string{"Linux/UNIX"},
		StartTime:           &start, // a start time of now returns the current price per zone
		EndTime:             &start,
	}
	client := a.Spot(k.Region)
	latest := map[string]ec2types.SpotPrice{}
	for {
		out, err := client.DescribeSpotPriceHistory(ctx, in)
		if err != nil {
			return 0, fmt.Errorf("aws ec2 DescribeSpotPriceHistory %s: %w", k, err)
		}
		for _, sp := range out.SpotPriceHistory {
			az := aws.ToString(sp.AvailabilityZone)
			if prev, ok := latest[az]; ok && aws.ToTime(prev.Timestamp).After(aws.ToTime(sp.Timestamp)) {
				continue
			}
			latest[az] = sp
		}
		if out.NextToken == nil || *out.NextToken == "" {
			break
		}
		in.NextToken = out.NextToken
	}
	prices := map[string]float64{}
	for az, sp := range latest {
		v, err := strconv.ParseFloat(aws.ToString(sp.SpotPrice), 64)
		if err != nil {
			return 0, fmt.Errorf("spot price %q for %s: %w", aws.ToString(sp.SpotPrice), az, err)
		}
		if v > 0 {
			prices[az] = v
		}
	}
	if v, ok := prices[k.Zone]; ok {
		return v, nil
	}
	if len(prices) == 0 {
		return 0, fmt.Errorf("%s: no spot price history: %w", k, ErrNotFound)
	}
	var sum float64
	for _, v := range prices {
		sum += v
	}
	return sum / float64(len(prices)), nil
}
