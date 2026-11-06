package synth

import (
	"sort"
	"strings"
)

// InstanceType is a node shape: capacity and allocatable as kubelet/EKS report them.
type InstanceType struct {
	CPU, Memory                       float64 // capacity: cores, bytes
	AllocatableCPU, AllocatableMemory float64 // after system/kube reserved and eviction threshold
	Pods                              float64 // max pods (EKS ENI limit)
	GPUs                              int     // NVIDIA GPUs (nvidia.com/gpu capacity), 0 for CPU-only types
	GPUModel                          string  // DCGM modelName of those GPUs
}

const gib = 1024 * 1024 * 1024
const mib = 1024 * 1024

// InstanceTypes known to the generator. The default scenarios only use m6i.xlarge and
// c6a.2xlarge, which WO-03's dev price table covers.
var InstanceTypes = map[string]InstanceType{
	"m6i.xlarge":  {CPU: 4, Memory: 16 * gib, AllocatableCPU: 3.92, AllocatableMemory: 14.8 * gib, Pods: 58},
	"m6i.2xlarge": {CPU: 8, Memory: 32 * gib, AllocatableCPU: 7.91, AllocatableMemory: 29.9 * gib, Pods: 58},
	"c6a.2xlarge": {CPU: 8, Memory: 16 * gib, AllocatableCPU: 7.91, AllocatableMemory: 14.8 * gib, Pods: 58},
	"r6i.xlarge":  {CPU: 4, Memory: 32 * gib, AllocatableCPU: 3.92, AllocatableMemory: 29.9 * gib, Pods: 58},
	// GPU types: one NVIDIA A10G (24 GiB) per 4 vCPU / 16 GiB, g5.12xlarge has four.
	"g5.xlarge":   {CPU: 4, Memory: 16 * gib, AllocatableCPU: 3.92, AllocatableMemory: 14.8 * gib, Pods: 58, GPUs: 1, GPUModel: "NVIDIA A10G"},
	"g5.12xlarge": {CPU: 48, Memory: 192 * gib, AllocatableCPU: 47.9, AllocatableMemory: 180 * gib, Pods: 234, GPUs: 4, GPUModel: "NVIDIA A10G"},
}

func knownInstanceTypes() string {
	var s []string
	for k := range InstanceTypes {
		s = append(s, k)
	}
	sort.Strings(s)
	return strings.Join(s, ", ")
}
