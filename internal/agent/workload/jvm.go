package workload

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
)

// JVM detection sources (hub API JVMContainer.detectedBy).
const (
	JVMByImage   = "image"
	JVMByCommand = "command"
	JVMByEnv     = "env"
)

// jvmOptionVars are the environment variables the JVM (or its start scripts) read options from,
// lowest precedence first: the JVM applies JAVA_TOOL_OPTIONS, then JDK_JAVA_OPTIONS (java launcher),
// then the command line (start scripts put JAVA_OPTS on it), and _JAVA_OPTIONS overrides all.
var jvmOptionVars = []string{"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "JAVA_OPTS"}

const jvmOverrideVar = "_JAVA_OPTIONS"

// jvmImage matches JVM base images by name or tag token, e.g. eclipse-temurin:21-jre,
// amazoncorretto:21, app:1.4.0-temurin21-jre, openjdk:17-slim.
var jvmImage = regexp.MustCompile(`(?i)(^|[/:_.@-])(java|openjdk|jdk|jre|temurin|corretto|amazoncorretto|zulu|graalvm|semeru|liberica|adoptopenjdk)[0-9]*($|[/:_.@-])`)

// JVMImage reports whether an image name looks like a JVM image.
func JVMImage(image string) bool { return jvmImage.MatchString(image) }

// JVM are the heap settings of a JVM container. Zero values mean "not set" (JVM default).
type JVM struct {
	Container  string
	DetectedBy []string
	// MaxHeap (-Xmx, -XX:MaxHeapSize), InitialHeap (-Xms, -XX:InitialHeapSize) and MaxRAM
	// (-XX:MaxRAM) in bytes.
	MaxHeap, InitialHeap, MaxRAM int64
	// MaxRAMPercentage is -XX:MaxRAMPercentage, or 100 / -XX:MaxRAMFraction.
	MaxRAMPercentage float64
	// Unresolved are option variables set through valueFrom (ConfigMap/Secret), which are not read.
	Unresolved []string
}

// FixedHeap reports whether the maximum heap is fixed (-Xmx), so the container cannot use more
// memory and needs a heap change to use less.
func (j JVM) FixedHeap() bool { return j.MaxHeap > 0 }

// DetectJVM reports whether a container runs a JVM and parses its heap settings from the option
// variables and the command line. A container is a JVM if its image looks like one, its command
// runs java or passes JVM heap options, or it sets one of the JVM option variables.
func DetectJVM(c corev1.Container) (JVM, bool) {
	j := JVM{Container: c.Name}
	if JVMImage(c.Image) {
		j.DetectedBy = append(j.DetectedBy, JVMByImage)
	}
	var cmd []string
	for _, s := range append(slices.Clone(c.Command), c.Args...) {
		cmd = append(cmd, strings.Fields(s)...) // sh -c "java -Xmx1g ..."
	}
	for _, t := range cmd {
		base := t[strings.LastIndex(t, "/")+1:]
		if base == "java" || heapOption(t) {
			j.DetectedBy = append(j.DetectedBy, JVMByCommand)
			break
		}
	}
	env := map[string]string{}
	envSet := false
	for _, e := range c.Env {
		if e.Name != jvmOverrideVar && !slices.Contains(jvmOptionVars, e.Name) {
			continue
		}
		envSet = true
		if e.ValueFrom != nil {
			j.Unresolved = append(j.Unresolved, e.Name)
			continue
		}
		env[e.Name] = e.Value
	}
	if envSet {
		j.DetectedBy = append(j.DetectedBy, JVMByEnv)
	}
	if len(j.DetectedBy) == 0 {
		return JVM{}, false
	}
	for _, name := range jvmOptionVars {
		j.apply(strings.Fields(env[name]))
	}
	j.apply(cmd)
	j.apply(strings.Fields(env[jvmOverrideVar]))
	return j, true
}

// heapOption reports whether a token is a heap-sizing option.
func heapOption(t string) bool {
	for _, p := range []string{"-Xmx", "-Xms", "-XX:MaxHeapSize=", "-XX:InitialHeapSize=", "-XX:MaxRAMPercentage=", "-XX:MaxRAMFraction=", "-XX:MaxRAM="} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// apply parses options in order; later options win, as in the JVM. Invalid values are ignored.
func (j *JVM) apply(opts []string) {
	for _, o := range opts {
		switch {
		case strings.HasPrefix(o, "-Xmx"):
			setSize(&j.MaxHeap, o[len("-Xmx"):])
		case strings.HasPrefix(o, "-XX:MaxHeapSize="):
			setSize(&j.MaxHeap, o[len("-XX:MaxHeapSize="):])
		case strings.HasPrefix(o, "-Xms"):
			setSize(&j.InitialHeap, o[len("-Xms"):])
		case strings.HasPrefix(o, "-XX:InitialHeapSize="):
			setSize(&j.InitialHeap, o[len("-XX:InitialHeapSize="):])
		case strings.HasPrefix(o, "-XX:MaxRAMPercentage="):
			if v, err := strconv.ParseFloat(o[len("-XX:MaxRAMPercentage="):], 64); err == nil && v > 0 && v <= 100 {
				j.MaxRAMPercentage = v
			}
		case strings.HasPrefix(o, "-XX:MaxRAMFraction="):
			if v, err := strconv.Atoi(o[len("-XX:MaxRAMFraction="):]); err == nil && v > 0 {
				j.MaxRAMPercentage = 100 / float64(v)
			}
		case strings.HasPrefix(o, "-XX:MaxRAM="):
			setSize(&j.MaxRAM, o[len("-XX:MaxRAM="):])
		}
	}
}

func setSize(dst *int64, s string) {
	if v, ok := ParseJVMSize(s); ok {
		*dst = v
	}
}

// ParseJVMSize parses a JVM size ("128m", "2G", "1048576", "512k") into bytes.
func ParseJVMSize(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		mult = 1 << 10
	case 'm', 'M':
		mult = 1 << 20
	case 'g', 'G':
		mult = 1 << 30
	case 't', 'T':
		mult = 1 << 40
	}
	if mult > 1 {
		s = s[:len(s)-1]
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 || v > (1<<62)/mult {
		return 0, false
	}
	return v * mult, true
}

// Report returns the hub API form.
func (j JVM) Report() hubclient.JVMContainer {
	q := func(v int64) string {
		if v <= 0 {
			return ""
		}
		return resource.NewQuantity(v, resource.BinarySI).String()
	}
	return hubclient.JVMContainer{
		Name: j.Container, DetectedBy: slices.Clone(j.DetectedBy),
		MaxHeap: q(j.MaxHeap), InitialHeap: q(j.InitialHeap), MaxRAM: q(j.MaxRAM),
		MaxRAMPercentage: j.MaxRAMPercentage, Unresolved: slices.Clone(j.Unresolved),
	}
}

// JVMReport returns the JVM containers of a workload for the hub status report; nil when detection
// is off or there are none.
func JVMReport(containers []Container, detect bool) []hubclient.JVMContainer {
	if !detect {
		return nil
	}
	var out []hubclient.JVMContainer
	for _, c := range containers {
		if j, ok := DetectJVM(c.Container); ok {
			out = append(out, j.Report())
		}
	}
	return out
}

// JVMFixedHeapRefusal reports whether a memory change of container c must be refused because it is
// a JVM with a fixed maximum heap: detected live (when detection is on) or flagged by the hub.
// The message explains the refusal.
func JVMFixedHeapRefusal(c corev1.Container, detect, hubFlagged bool) (string, bool) {
	if detect {
		if j, ok := DetectJVM(c); ok && j.FixedHeap() {
			return fmt.Sprintf("container %s is a JVM with a fixed maximum heap (%s); memory is audit-only: change -Xmx together with the memory",
				c.Name, resource.NewQuantity(j.MaxHeap, resource.BinarySI)), true
		}
	}
	if hubFlagged {
		return fmt.Sprintf("container %s is flagged %s by the hub; memory is audit-only", c.Name, v1alpha1.RiskJVMFixedHeap), true
	}
	return "", false
}
