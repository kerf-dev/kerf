package synth

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sort"
)

// rnd is a deterministic random source. Every entity derives its own stream from the scenario
// seed and its identity, so adding a workload does not change the data of the others.
type rnd struct{ *rand.Rand }

func newRand(seed int64, parts ...string) *rnd {
	h := fnv.New64a()
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(seed))
	h.Write(b[:])
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	s := h.Sum64()
	return &rnd{rand.New(rand.NewPCG(s, s^0x9e3779b97f4a7c15))}
}

func (r *rnd) uniform(a, b float64) float64 { return a + r.Float64()*(b-a) }

func (r *rnd) chance(p float64) bool { return r.Float64() < p }

func (r *rnd) choice(xs []string) string { return xs[r.IntN(len(xs))] }

// pick returns a key of m chosen with probability proportional to its weight.
func (r *rnd) pick(m map[string]float64) string {
	keys := make([]string, 0, len(m))
	total := 0.0
	for k, w := range m {
		if w > 0 {
			keys = append(keys, k)
			total += w
		}
	}
	sort.Strings(keys)
	x := r.Float64() * total
	for _, k := range keys {
		x -= m[k]
		if x < 0 {
			return k
		}
	}
	return keys[len(keys)-1]
}

// pickF picks a float value with weights given as alternating value, weight pairs.
func (r *rnd) pickF(pairs ...float64) float64 {
	total := 0.0
	for i := 1; i < len(pairs); i += 2 {
		total += pairs[i]
	}
	x := r.Float64() * total
	for i := 0; i+1 < len(pairs); i += 2 {
		x -= pairs[i+1]
		if x < 0 {
			return pairs[i]
		}
	}
	return pairs[len(pairs)-2]
}

func (r *rnd) poisson(mean float64) int {
	if mean <= 0 {
		return 0
	}
	l, k, p := math.Exp(-mean), 0, 1.0
	for {
		p *= r.Float64()
		if p <= l {
			return k
		}
		k++
	}
}

// expo returns an exponentially distributed value with the given mean.
func (r *rnd) expo(mean float64) float64 { return r.ExpFloat64() * mean }

const k8sAlphabet = "bcdfghjklmnpqrstvwxz2456789"

// suffix returns n characters from the alphabet Kubernetes uses for generated names.
func (r *rnd) suffix(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = k8sAlphabet[r.IntN(len(k8sAlphabet))]
	}
	return string(b)
}

func (r *rnd) hex(n int) string {
	const h = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = h[r.IntN(16)]
	}
	return string(b)
}

func (r *rnd) uid() string {
	return fmt.Sprintf("%s-%s-4%s-%c%s-%s", r.hex(8), r.hex(4), r.hex(3), "89ab"[r.IntN(4)], r.hex(3), r.hex(12))
}
