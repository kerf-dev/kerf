package recommender

import (
	"fmt"
	"math"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
)

// FormatCPU formats cores as a Kubernetes quantity: whole cores as "2", otherwise millicores "250m".
func FormatCPU(cores float64) string {
	m := int64(math.Round(cores * 1000))
	if m%1000 == 0 {
		return strconv.FormatInt(m/1000, 10)
	}
	return strconv.FormatInt(m, 10) + "m"
}

// FormatMemory formats bytes with the largest binary suffix that divides the value exactly.
func FormatMemory(bytes float64) string {
	b := int64(math.Round(bytes))
	for _, u := range []struct {
		suffix string
		size   int64
	}{{"Ti", 1 << 40}, {"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10}} {
		if b != 0 && b%u.size == 0 {
			return strconv.FormatInt(b/u.size, 10) + u.suffix
		}
	}
	return strconv.FormatInt(b, 10)
}

// ParseCPU parses a CPU quantity into cores.
func ParseCPU(s string) (float64, error) {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, err
	}
	return float64(q.MilliValue()) / 1000, nil
}

// ParseMemory parses a memory quantity into bytes.
func ParseMemory(s string) (float64, error) {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, err
	}
	return float64(q.Value()), nil
}

// roundUp rounds v up to a multiple of step, tolerating floating-point noise.
func roundUp(v, step float64) float64 {
	n := v / step
	r := math.Round(n)
	if math.Abs(n-r) < 1e-9 {
		return r * step
	}
	return math.Ceil(n) * step
}

func parseRatio(s string) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty ratio")
	}
	return strconv.ParseFloat(s, 64)
}
