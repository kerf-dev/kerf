package pricing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// replay serves recorded AWS responses from testdata/aws to the real SDK clients.
//
// Pricing: instance type "throttle.large" returns a throttling error; a request with a NextToken gets
// "<type>-page2.json". EC2: region "eu-throttle-1" returns a throttling error.
type replay struct {
	t     *testing.T
	mu    sync.Mutex
	calls map[string]int // "pricing" / "ec2" -> count
	seen  []string       // request descriptions, for assertions
}

func (r *replay) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	host := req.URL.Host
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	defer r.mu.Unlock()
	switch {
	case strings.HasPrefix(host, "api.pricing."):
		r.calls["pricing"]++
		var in struct {
			Filters   []struct{ Field, Value, Type string }
			NextToken *string
		}
		if err := json.Unmarshal(body, &in); err != nil {
			r.t.Fatalf("pricing request body: %v", err)
		}
		filters := map[string]string{}
		for _, f := range in.Filters {
			filters[f.Field] = f.Value
		}
		r.seen = append(r.seen, fmt.Sprintf("pricing %s %v", req.Header.Get("X-Amz-Target"), filters))
		it := filters["instanceType"]
		if it == "throttle.large" {
			return r.file(http.StatusBadRequest, "application/x-amz-json-1.1", "pricing/error-throttling.json"), nil
		}
		name := it
		if in.NextToken != nil {
			name += "-page2"
		}
		if _, err := os.Stat(filepath.Join("testdata/aws/pricing", name+".json")); err != nil {
			name = "empty"
		}
		return r.file(http.StatusOK, "application/x-amz-json-1.1", "pricing/"+name+".json"), nil
	case strings.HasPrefix(host, "ec2."):
		r.calls["ec2"]++
		form, _ := url.ParseQuery(string(body))
		region := strings.Split(host, ".")[1]
		r.seen = append(r.seen, fmt.Sprintf("ec2 %s %s %s %s", region, form.Get("Action"), form.Get("InstanceType.1"), form.Get("ProductDescription.1")))
		if region == "eu-throttle-1" {
			return r.file(http.StatusServiceUnavailable, "text/xml", "ec2/error-throttling.xml"), nil
		}
		path := filepath.Join("ec2", region, form.Get("InstanceType.1")+".xml")
		if _, err := os.Stat(filepath.Join("testdata/aws", path)); err != nil {
			path = "ec2/eu-west-1/empty.xml"
		}
		return r.file(http.StatusOK, "text/xml", path), nil
	}
	r.t.Fatalf("unexpected request to %s", req.URL)
	return nil, nil
}

func (r *replay) file(status int, contentType, name string) *http.Response {
	b, err := os.ReadFile(filepath.Join("testdata/aws", name))
	if err != nil {
		r.t.Fatal(err)
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {contentType}, "X-Amzn-Requestid": {"test"}},
		Body:       io.NopCloser(bytes.NewReader(b)),
	}
}

func newReplayAWS(t *testing.T) (*AWS, *replay) {
	t.Helper()
	rt := &replay{t: t}
	cfg := aws.Config{
		Region:      "eu-west-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIDTEST", "secret", ""),
		HTTPClient:  &http.Client{Transport: rt},
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
	}
	p := NewAWS(cfg, "")
	p.Now = func() time.Time { return time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC) }
	return p, rt
}

func TestAWSPrice(t *testing.T) {
	tests := []struct {
		name    string
		key     Key
		want    Price
		wantErr error // errors.Is target; nil = success
		errText string
	}{
		{
			name: "on-demand m6i.xlarge",
			key:  Key{Region: "eu-west-1", Zone: "eu-west-1a", InstanceType: "m6i.xlarge", CapacityType: OnDemand},
			want: Price{HourlyUSD: 0.214, Shape: Shape{VCPU: 4, MemoryGiB: 16}, Source: SourceList},
		},
		{
			name: "spot c6a.2xlarge uses the zone's latest price",
			key:  Key{Region: "eu-west-1", Zone: "eu-west-1b", InstanceType: "c6a.2xlarge", CapacityType: Spot},
			want: Price{HourlyUSD: 0.1356, Shape: Shape{VCPU: 8, MemoryGiB: 16}, Source: SourceSpot},
		},
		{
			name: "spot without zone averages zones",
			key:  Key{Region: "eu-west-1", InstanceType: "c6a.2xlarge", CapacityType: Spot},
			want: Price{HourlyUSD: (0.1329 + 0.1356 + 0.1386) / 3, Shape: Shape{VCPU: 8, MemoryGiB: 16}, Source: SourceSpot},
		},
		{
			name: "spot in a zone AWS does not report averages the others",
			key:  Key{Region: "eu-west-1", Zone: "eu-west-1b", InstanceType: "m6i.xlarge", CapacityType: Spot},
			want: Price{HourlyUSD: (0.0798 + 0.0817) / 2, Shape: Shape{VCPU: 4, MemoryGiB: 16}, Source: SourceSpot},
		},
		{
			name: "paginated GetProducts",
			key:  Key{Region: "eu-west-1", InstanceType: "m7i.large", CapacityType: OnDemand},
			want: Price{HourlyUSD: 0.1124, Shape: Shape{VCPU: 2, MemoryGiB: 8}, Source: SourceList},
		},
		{
			name:    "unknown instance type",
			key:     Key{Region: "eu-west-1", InstanceType: "x99.huge", CapacityType: OnDemand},
			wantErr: ErrNotFound,
		},
		{
			name:    "no spot history",
			key:     Key{Region: "eu-west-1", InstanceType: "r6i.xlarge", CapacityType: Spot},
			wantErr: ErrNotFound,
		},
		{
			name:    "missing region",
			key:     Key{InstanceType: "m6i.xlarge", CapacityType: OnDemand},
			wantErr: ErrNotFound,
		},
		{
			name:    "malformed memory attribute",
			key:     Key{Region: "eu-west-1", InstanceType: "m6i.large", CapacityType: OnDemand},
			errText: `memory "8 GB"`,
		},
		{
			name:    "pricing throttled",
			key:     Key{Region: "eu-west-1", InstanceType: "throttle.large", CapacityType: OnDemand},
			errText: "ThrottlingException",
		},
		{
			name:    "ec2 throttled",
			key:     Key{Region: "eu-throttle-1", InstanceType: "m6i.xlarge", CapacityType: Spot},
			errText: "RequestLimitExceeded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := newReplayAWS(t)
			got, err := p.Price(context.Background(), tt.key)
			switch {
			case tt.wantErr != nil || tt.errText != "":
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Fatalf("error %v is not %v", err, tt.wantErr)
				}
				if tt.wantErr == nil && errors.Is(err, ErrNotFound) {
					t.Fatalf("transient error %v must not be ErrNotFound", err)
				}
				if !strings.Contains(err.Error(), tt.errText) {
					t.Fatalf("error %q does not contain %q", err, tt.errText)
				}
			case err != nil:
				t.Fatal(err)
			default:
				if math.Abs(got.HourlyUSD-tt.want.HourlyUSD) > 1e-9 || got.Shape != tt.want.Shape {
					t.Fatalf("got %+v, want %+v", got, tt.want)
				}
			}
		})
	}
}

func TestAWSRequests(t *testing.T) {
	p, rt := newReplayAWS(t)
	if p.Name() != "aws" {
		t.Fatalf("name %q", p.Name())
	}
	if _, err := p.Price(context.Background(), Key{Region: "eu-west-1", Zone: "eu-west-1b", InstanceType: "c6a.2xlarge", CapacityType: Spot}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"pricing AWSPriceListService.GetProducts map[capacitystatus:Used instanceType:c6a.2xlarge licenseModel:No License required marketoption:OnDemand operatingSystem:Linux preInstalledSw:NA regionCode:eu-west-1 tenancy:Shared]",
		"ec2 eu-west-1 DescribeSpotPriceHistory c6a.2xlarge Linux/UNIX",
	}
	if strings.Join(rt.seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(rt.seen, "\n"), strings.Join(want, "\n"))
	}
}

// The AWS provider behind the cache serves every lookup in a refresh window from memory.
func TestAWSCachedAndWeight(t *testing.T) {
	p, rt := newReplayAWS(t)
	c := NewCached(p, 24*time.Hour, nil)
	s := &Splitter{Provider: c}
	ctx := context.Background()
	for range 3 {
		if _, err := c.Price(ctx, Key{Region: "eu-west-1", Zone: "eu-west-1a", InstanceType: "m6i.xlarge", CapacityType: OnDemand}); err != nil {
			t.Fatal(err)
		}
		w, src := s.Weight(ctx, "eu-west-1", "m6i.xlarge")
		if src != WeightDerived || w < 8 || w > 12 {
			t.Fatalf("weight %v (%s), want derived in [8,12]", w, src)
		}
	}
	// m6i.xlarge once, c6i.xlarge and r6i.xlarge once each for the weight.
	if rt.calls["pricing"] != 3 {
		t.Fatalf("pricing calls = %d, want 3", rt.calls["pricing"])
	}
}

func TestParseMemoryGiB(t *testing.T) {
	tests := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"16 GiB", 16, true},
		{"0.5 GiB", 0.5, true},
		{"1,024 GiB", 1024, true},
		{"16 GB", 0, false},
		{"NA", 0, false},
		{"-1 GiB", 0, false},
	}
	for _, tt := range tests {
		got, err := parseMemoryGiB(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("parseMemoryGiB(%q) = %v, %v; want %v ok=%v", tt.in, got, err, tt.want, tt.ok)
		}
	}
}

func TestParsePriceListItemErrors(t *testing.T) {
	tests := []struct{ name, doc, errText string }{
		{"not json", `{`, "decode"},
		{"other instance type", `{"product":{"attributes":{"instanceType":"m5.large"}}}`, "want \"m6i.xlarge\""},
		{"bad vcpu", `{"product":{"attributes":{"instanceType":"m6i.xlarge","vcpu":"four","memory":"16 GiB"}}}`, "vcpu"},
		{"bad price", `{"product":{"attributes":{"instanceType":"m6i.xlarge","vcpu":"4","memory":"16 GiB"}},` +
			`"terms":{"OnDemand":{"a":{"priceDimensions":{"b":{"unit":"Hrs","pricePerUnit":{"USD":"x"}}}}}}}`, "price"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parsePriceListItem(tt.doc, "m6i.xlarge")
			if err == nil || !strings.Contains(err.Error(), tt.errText) {
				t.Fatalf("err = %v, want containing %q", err, tt.errText)
			}
		})
	}
	// Non-hourly dimensions are ignored.
	p, err := parsePriceListItem(`{"product":{"attributes":{"instanceType":"m6i.xlarge","vcpu":"4","memory":"16 GiB"}},`+
		`"terms":{"OnDemand":{"a":{"priceDimensions":{"b":{"unit":"Quantity","pricePerUnit":{"USD":"99"}}}}}}}`, "m6i.xlarge")
	if err != nil || p.HourlyUSD != 0 {
		t.Fatalf("got %+v, %v; want zero price", p, err)
	}
}
