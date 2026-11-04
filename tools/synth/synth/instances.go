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
}

func knownInstanceTypes() string {
	var s []string
	for k := range InstanceTypes {
		s = append(s, k)
	}
	sort.Strings(s)
	return strings.Join(s, ", ")
}
