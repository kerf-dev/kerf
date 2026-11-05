package workload

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

const mib = 1 << 20

func env(kv ...string) []corev1.EnvVar {
	var out []corev1.EnvVar
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, corev1.EnvVar{Name: kv[i], Value: kv[i+1]})
	}
	return out
}

func TestDetectJVM(t *testing.T) {
	tests := []struct {
		name string
		c    corev1.Container
		want *JVM // nil: not a JVM
	}{
		{name: "dev jvm-maxram: image + JAVA_TOOL_OPTIONS", c: corev1.Container{
			Image: "eclipse-temurin:21.0.13_9-jdk-alpine-3.23", Command: []string{"java", "/app/Load.java"},
			Env: env("JAVA_TOOL_OPTIONS", "-XX:MaxRAMPercentage=75.0 -XX:+UseSerialGC -XX:TieredStopAtLevel=1", "LIVE_MB", "64")},
			want: &JVM{DetectedBy: []string{JVMByImage, JVMByCommand, JVMByEnv}, MaxRAMPercentage: 75}},
		{name: "dev jvm-xmx: -Xmx on the command line", c: corev1.Container{
			Image: "eclipse-temurin:21.0.13_9-jdk-alpine-3.23", Command: []string{"java", "-Xmx128m", "-XX:+UseSerialGC", "/app/Load.java"}},
			want: &JVM{DetectedBy: []string{JVMByImage, JVMByCommand}, MaxHeap: 128 * mib}},
		{name: "command line overrides JAVA_TOOL_OPTIONS, _JAVA_OPTIONS overrides all", c: corev1.Container{
			Image: "registry/app:1.0", Args: []string{"-Xmx1g", "-Xms256m", "-jar", "app.jar"},
			Env: env("JAVA_TOOL_OPTIONS", "-Xmx512m -XX:MaxRAM=4g", "_JAVA_OPTIONS", "-Xms512m")},
			want: &JVM{DetectedBy: []string{JVMByCommand, JVMByEnv}, MaxHeap: 1 << 30, InitialHeap: 512 * mib, MaxRAM: 4 << 30}},
		{name: "sh -c wrapper with JAVA_OPTS", c: corev1.Container{
			Image: "registry/app:1.0", Command: []string{"sh", "-c", "exec /opt/java/bin/java $JAVA_OPTS -jar app.jar"},
			Env: env("JAVA_OPTS", "-XX:MaxHeapSize=2147483648 -XX:InitialHeapSize=64M")},
			want: &JVM{DetectedBy: []string{JVMByCommand, JVMByEnv}, MaxHeap: 2 << 30, InitialHeap: 64 * mib}},
		{name: "JDK_JAVA_OPTIONS beats JAVA_TOOL_OPTIONS; MaxRAMFraction", c: corev1.Container{
			Image: "amazoncorretto:21", Env: env("JAVA_TOOL_OPTIONS", "-XX:MaxRAMPercentage=50", "JDK_JAVA_OPTIONS", "-XX:MaxRAMFraction=4")},
			want: &JVM{DetectedBy: []string{JVMByImage, JVMByEnv}, MaxRAMPercentage: 25}},
		{name: "image only: JVM defaults", c: corev1.Container{Image: "registry/payments/checkout:1.4.0-temurin21-jre"},
			want: &JVM{DetectedBy: []string{JVMByImage}}},
		{name: "valueFrom is unresolved", c: corev1.Container{Image: "registry/app:1.0",
			Env: []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "opts"}}}}},
			want: &JVM{DetectedBy: []string{JVMByEnv}, Unresolved: []string{"JAVA_TOOL_OPTIONS"}}},
		{name: "invalid values ignored, earlier valid value kept", c: corev1.Container{Image: "openjdk:17",
			Env: env("JAVA_TOOL_OPTIONS", "-Xmx256m -XX:MaxRAMPercentage=75"), Args: []string{"-Xmxlots", "-XX:MaxRAMPercentage=150", "-XX:MaxRAMFraction=0"}},
			want: &JVM{DetectedBy: []string{JVMByImage, JVMByCommand, JVMByEnv}, MaxHeap: 256 * mib, MaxRAMPercentage: 75}},
		{name: "not a JVM", c: corev1.Container{Image: "nginx:1.27", Command: []string{"nginx", "-g", "daemon off;"}, Env: env("JAVA_HOME_UNUSED", "x")}},
		{name: "python is heap-bound but not a JVM", c: corev1.Container{Image: "python:3.13", Env: env("PYTHONUNBUFFERED", "1")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.c.Name = "app"
			got, ok := DetectJVM(tt.c)
			if tt.want == nil {
				if ok {
					t.Fatalf("detected %+v, want not a JVM", got)
				}
				return
			}
			tt.want.Container = "app"
			if !ok || !reflect.DeepEqual(got, *tt.want) {
				t.Errorf("DetectJVM = %+v (%v), want %+v", got, ok, *tt.want)
			}
		})
	}
}

func TestParseJVMSize(t *testing.T) {
	for s, want := range map[string]int64{
		"128m": 128 * mib, "128M": 128 * mib, "2g": 2 << 30, "1T": 1 << 40, "512k": 512 << 10, "1048576": 1 << 20,
		"": 0, "m": 0, "-1g": 0, "0": 0, "1.5g": 0, "99999999999t": 0,
	} {
		got, ok := ParseJVMSize(s)
		if got != want || ok != (want > 0) {
			t.Errorf("ParseJVMSize(%q) = %d %v, want %d", s, got, ok, want)
		}
	}
}

func TestJVMReport(t *testing.T) {
	cs := []Container{
		{Container: corev1.Container{Name: "app", Image: "eclipse-temurin:21-jre", Command: []string{"java", "-Xmx1536m", "-Xms512m", "-XX:MaxRAM=3g", "-jar", "a.jar"}}},
		{Container: corev1.Container{Name: "sidecar", Image: "envoyproxy/envoy:v1.34.1"}},
		{Container: corev1.Container{Name: "pct", Image: "openjdk:17", Env: env("JAVA_TOOL_OPTIONS", "-XX:MaxRAMPercentage=62.5")}},
	}
	want := []hubclient.JVMContainer{
		{Name: "app", DetectedBy: []string{"image", "command"}, MaxHeap: "1536Mi", InitialHeap: "512Mi", MaxRAM: "3Gi"},
		{Name: "pct", DetectedBy: []string{"image", "env"}, MaxRAMPercentage: 62.5},
	}
	if got := JVMReport(cs, true); !reflect.DeepEqual(got, want) {
		t.Errorf("JVMReport = %+v, want %+v", got, want)
	}
	if got := JVMReport(cs, false); got != nil {
		t.Errorf("JVMReport(detect off) = %+v, want nil", got)
	}
	if got := JVMReport(cs[1:2], true); got != nil {
		t.Errorf("JVMReport(no JVM) = %+v, want nil", got)
	}
}

func TestJVMFixedHeapRefusal(t *testing.T) {
	xmx := corev1.Container{Name: "app", Image: "eclipse-temurin:21-jre", Command: []string{"java", "-Xmx128m"}}
	pct := corev1.Container{Name: "app", Image: "eclipse-temurin:21-jre", Env: env("JAVA_TOOL_OPTIONS", "-XX:MaxRAMPercentage=75")}
	tests := []struct {
		name            string
		c               corev1.Container
		detect, flagged bool
		want            bool
		msg             string
	}{
		{name: "live -Xmx", c: xmx, detect: true, want: true, msg: "fixed maximum heap (128Mi)"},
		{name: "live -Xmx, detection off", c: xmx},
		{name: "hub flag", c: pct, detect: true, flagged: true, want: true, msg: "flagged jvm-fixed-heap"},
		{name: "hub flag, detection off", c: pct, flagged: true, want: true, msg: "flagged jvm-fixed-heap"},
		{name: "percentage", c: pct, detect: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, ok := JVMFixedHeapRefusal(tt.c, tt.detect, tt.flagged)
			if ok != tt.want || !strings.Contains(msg, tt.msg) {
				t.Errorf("= %q %v, want %v containing %q", msg, ok, tt.want, tt.msg)
			}
		})
	}
}

func TestJVMImageAgreesWithHeapBound(t *testing.T) {
	for _, img := range []string{"eclipse-temurin:21-jre", "amazoncorretto:21", "registry/app:1.4.0-temurin21-jre", "openjdk:17-slim"} {
		if !JVMImage(img) || !HeapBound(corev1.Container{Image: img}) {
			t.Errorf("%s: JVMImage=%v HeapBound=%v, want both true", img, JVMImage(img), HeapBound(corev1.Container{Image: img}))
		}
	}
}
