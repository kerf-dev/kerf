package hubapi

// JVM detection sources (JVMContainer.DetectedBy).
const (
	JVMDetectedByImage   = "image"
	JVMDetectedByCommand = "command"
	JVMDetectedByEnv     = "env"
)

// JVM heap sizing modes (JVMExplanation.HeapSizing).
const (
	JVMHeapFixed      = "fixed"
	JVMHeapPercentage = "percentage"
	JVMHeapDefault    = "default"
)

// JVMContainer are the heap settings of one JVM container, as detected by the agent (WO-12).
// Empty values mean "not set" (JVM default).
type JVMContainer struct {
	Name       string   `json:"name"`
	DetectedBy []string `json:"detectedBy"`
	// MaxHeap is -Xmx / -XX:MaxHeapSize, InitialHeap -Xms / -XX:InitialHeapSize, MaxRAM -XX:MaxRAM
	// (Kubernetes quantities).
	MaxHeap     string `json:"maxHeap,omitempty"`
	InitialHeap string `json:"initialHeap,omitempty"`
	MaxRAM      string `json:"maxRAM,omitempty"`
	// MaxRAMPercentage is -XX:MaxRAMPercentage (or 100 / -XX:MaxRAMFraction); 0 = not set.
	MaxRAMPercentage float64 `json:"maxRAMPercentage,omitempty"`
	// Unresolved lists option variables set from ConfigMap/Secret references (not read).
	Unresolved []string `json:"unresolved,omitempty"`
}

// JVMExplanation explains how the JVM heap shaped a memory recommendation (WO-12).
type JVMExplanation struct {
	HeapSizing       string   `json:"heapSizing"`
	DetectedBy       []string `json:"detectedBy"`
	Source           string   `json:"source,omitempty"`
	MaxRAMPercentage float64  `json:"maxRAMPercentage,omitempty"`
	MaxHeapBefore    string   `json:"maxHeapBefore,omitempty"`
	MaxHeapAfter     string   `json:"maxHeapAfter,omitempty"`
	HeapNeed         string   `json:"heapNeed,omitempty"`
	NonHeap          string   `json:"nonHeap,omitempty"`
	Floor            string   `json:"floor,omitempty"`
}

// JVM explanation sources.
const (
	JVMSourceAgent = "agent"
	JVMSourceImage = "image"
)
