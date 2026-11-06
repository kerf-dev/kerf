package store

import (
	"context"
	"encoding/json"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// The JVM containers an agent reports are stored in workload_status.settings under the key
// "jvm", next to the ValueSettings fields, so no schema migration is needed. ValueSettings decoding
// ignores the extra key.
type statusSettingsColumn struct {
	hubapi.ValueSettings
	JVM []hubapi.JVMContainer `json:"jvm,omitempty"`
	// Override is WorkloadStatus.override, stored the same way.
	Override string `json:"override,omitempty"`
}

// statusSettings returns the workload_status.settings value of a reported status.
func statusSettings(it hubapi.WorkloadStatus) []byte {
	var col statusSettingsColumn
	if it.Settings != nil {
		col.ValueSettings = *it.Settings
	}
	col.JVM = it.JVM
	col.Override = it.Override
	b, _ := json.Marshal(col)
	return b
}

// statusJVM returns the JVM containers stored in a workload_status.settings value.
func statusJVM(b []byte) []hubapi.JVMContainer {
	var col statusSettingsColumn
	if json.Unmarshal(b, &col) != nil {
		return nil
	}
	return col.JVM
}

// statusOverride returns the override name stored in a workload_status.settings value.
func statusOverride(b []byte) string {
	var col statusSettingsColumn
	if json.Unmarshal(b, &col) != nil {
		return ""
	}
	return col.Override
}

// ReportedJVM returns the JVM containers per workload the agent reported a status for. A workload
// with a status but no JVM containers maps to an empty slice; a workload without a status is absent.
func (s *Store) ReportedJVM(ctx context.Context, cluster string) (map[hubapi.WorkloadKey][]hubapi.JVMContainer, error) {
	rows, err := s.Pool.Query(ctx, `SELECT w.namespace, w.kind, w.name, ws.settings FROM workload_status ws
		JOIN workloads w ON w.id = ws.workload_id WHERE w.cluster = $1`, cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[hubapi.WorkloadKey][]hubapi.JVMContainer{}
	for rows.Next() {
		k := hubapi.WorkloadKey{Cluster: cluster}
		var b []byte
		if err := rows.Scan(&k.Namespace, &k.Kind, &k.Name, &b); err != nil {
			return nil, err
		}
		out[k] = append([]hubapi.JVMContainer{}, statusJVM(b)...)
	}
	return out, rows.Err()
}
