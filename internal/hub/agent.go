package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/hub/store"
)

const (
	maxBatchItems = 1000
	maxBodyBytes  = 8 << 20
)

// listRecommendations serves GET /v1/clusters/{cluster}/recommendations.
func (s *Server) listRecommendations(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	q := r.URL.Query()
	limit, err := intParam(q.Get("limit"), 0, 1, 5000)
	if err != nil {
		badRequest(w, r, "invalid_limit", err.Error())
		return
	}
	ns := q.Get("namespace")
	views, err := s.Store.LoadWorkloads(r.Context(), store.WorkloadFilter{Cluster: cluster})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	items := []hubapi.WorkloadRecommendation{}
	for _, v := range views {
		if v.Rec == nil || (ns != "" && v.Key.Namespace != ns) {
			continue
		}
		items = append(items, v.Rec.WithoutExplanations())
	}
	// Keyset pagination on namespace/kind/name (views are sorted that way).
	start := 0
	if c := q.Get("continue"); c != "" {
		cur, err := decodeCursor(c)
		if err != nil {
			badRequest(w, r, "invalid_continue", "Malformed continue token.")
			return
		}
		for start < len(items) && refKey(items[start].Workload) <= cur.Key {
			start++
		}
	}
	items = items[start:]
	out := hubapi.RecommendationList{Cluster: cluster, Items: items}
	if limit > 0 && len(items) > limit {
		out.Items = items[:limit]
		out.Continue = encodeCursor(cursor{Key: refKey(items[limit-1].Workload)})
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|", cluster, ns, out.Continue)
	for _, it := range out.Items {
		h.Write([]byte(it.ID))
	}
	etag := `"` + hex.EncodeToString(h.Sum(nil))[:32] + `"`
	w.Header().Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func refKey(r hubapi.WorkloadRef) string { return r.Namespace + "\x00" + r.Kind + "\x00" + r.Name }

func etagMatches(header, etag string) bool {
	for _, p := range strings.Split(header, ",") {
		p = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p), "W/"))
		if p == etag || p == "*" {
			return true
		}
	}
	return false
}

// reportWorkloadStatus serves POST /v1/clusters/{cluster}/workload-status.
func (s *Server) reportWorkloadStatus(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	var batch hubapi.WorkloadStatusBatch
	if !decodeBody(w, r, &batch) {
		return
	}
	if len(batch.Items) > maxBatchItems {
		writeProblem(w, r, http.StatusRequestEntityTooLarge, "batch_too_large", fmt.Sprintf("At most %d items per batch.", maxBatchItems))
		return
	}
	for i, it := range batch.Items {
		if err := validateStatus(it); err != nil {
			badRequest(w, r, "invalid_item", fmt.Sprintf("items[%d]: %v", i, err))
			return
		}
	}
	if err := s.Store.UpsertStatuses(r.Context(), cluster, batch.Items, s.now()); err != nil {
		s.internal(w, r, err)
		return
	}
	if batch.AgentVersion != "" {
		s.heartbeat(r.Context(), cluster, batch.AgentVersion)
	}
	s.Metrics.markDirty()
	writeJSON(w, http.StatusOK, hubapi.BatchResult{Accepted: len(batch.Items)})
}

// reportEvents serves POST /v1/clusters/{cluster}/events.
func (s *Server) reportEvents(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	var batch hubapi.EventBatch
	if !decodeBody(w, r, &batch) {
		return
	}
	if len(batch.Items) > maxBatchItems {
		writeProblem(w, r, http.StatusRequestEntityTooLarge, "batch_too_large", fmt.Sprintf("At most %d items per batch.", maxBatchItems))
		return
	}
	for i, e := range batch.Items {
		if err := validateEvent(e); err != nil {
			badRequest(w, r, "invalid_item", fmt.Sprintf("items[%d]: %v", i, err))
			return
		}
	}
	stored, dups, err := s.Store.InsertEvents(r.Context(), cluster, batch.Items)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, e := range stored {
		s.Metrics.countAction(cluster, e)
	}
	writeJSON(w, http.StatusOK, hubapi.BatchResult{Accepted: len(batch.Items) - dups, Duplicates: &dups})
}

// queryAudit serves GET /v1/audit.
func (s *Server) queryAudit(w http.ResponseWriter, r *http.Request) {
	tok := TokenFrom(r.Context())
	q := r.URL.Query()
	aq := store.AuditQuery{
		Cluster: q.Get("cluster"), Namespace: q.Get("namespace"), Kind: q.Get("kind"), Workload: q.Get("workload"), Types: q["type"],
	}
	if aq.Cluster != "" && !hubapi.ClusterNameRE.MatchString(aq.Cluster) {
		badRequest(w, r, "invalid_cluster", "Invalid cluster name.")
		return
	}
	if tok.Role == hubapi.RoleAgent {
		if aq.Cluster != "" && aq.Cluster != tok.Cluster {
			writeProblem(w, r, http.StatusForbidden, "cluster_forbidden", "Token is not allowed to access cluster "+aq.Cluster+".")
			return
		}
		aq.Cluster = tok.Cluster
	}
	if aq.Kind != "" && !hubapi.ValidKind(aq.Kind) {
		badRequest(w, r, "invalid_kind", "Invalid workload kind.")
		return
	}
	for _, t := range aq.Types {
		if !hubapi.Contains(hubapi.EventTypes, t) {
			badRequest(w, r, "invalid_type", "Invalid event type "+t+".")
			return
		}
	}
	var err error
	if aq.From, err = timeParam(q.Get("from")); err != nil {
		badRequest(w, r, "invalid_from", err.Error())
		return
	}
	if aq.To, err = timeParam(q.Get("to")); err != nil {
		badRequest(w, r, "invalid_to", err.Error())
		return
	}
	switch q.Get("order") {
	case "", "asc":
	case "desc":
		aq.Desc = true
	default:
		badRequest(w, r, "invalid_order", "order must be asc or desc.")
		return
	}
	if aq.Limit, err = intParam(q.Get("limit"), 100, 1, 1000); err != nil {
		badRequest(w, r, "invalid_limit", err.Error())
		return
	}
	if c := q.Get("continue"); c != "" {
		cur, err := decodeCursor(c)
		if err != nil || cur.ID == "" {
			badRequest(w, r, "invalid_continue", "Malformed continue token.")
			return
		}
		aq.AfterTime, aq.AfterID = &cur.Time, cur.ID
	}
	items, more, err := s.Store.QueryAudit(r.Context(), aq)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := hubapi.AuditEventList{Items: items}
	if more && len(items) > 0 {
		last := items[len(items)-1]
		out.Continue = encodeCursor(cursor{Time: last.Time, ID: last.ID})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- validation ----

var reasonRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

func validateRef(ref hubapi.WorkloadRef) error {
	if ref.Namespace == "" || ref.Name == "" || strings.Contains(ref.Namespace, "/") || strings.Contains(ref.Name, "/") {
		return errors.New("workload namespace and name are required")
	}
	if !hubapi.ValidKind(ref.Kind) {
		return fmt.Errorf("invalid workload kind %q", ref.Kind)
	}
	return nil
}

func validateStatus(it hubapi.WorkloadStatus) error {
	if err := validateRef(it.Workload); err != nil {
		return err
	}
	if !hubapi.Contains(hubapi.Modes, it.Modes.CPU) || !hubapi.Contains(hubapi.Modes, it.Modes.Memory) {
		return errors.New("modes.cpu and modes.memory must be off, audit or auto-patch")
	}
	for _, src := range []string{it.Modes.CPUSource, it.Modes.MemorySource} {
		if src != "" && !hubapi.Contains(hubapi.ModeSources, src) {
			return fmt.Errorf("invalid mode source %q", src)
		}
	}
	if !hubapi.Contains(hubapi.Phases, it.Phase) {
		return fmt.Errorf("invalid phase %q", it.Phase)
	}
	if it.Strategy != "" && !hubapi.Contains(hubapi.Strategies, it.Strategy) {
		return fmt.Errorf("invalid strategy %q", it.Strategy)
	}
	if it.RecommendationID != "" {
		if _, err := uuid.Parse(it.RecommendationID); err != nil {
			return errors.New("recommendationID must be a UUID")
		}
	}
	if s := it.Settings; s != nil {
		if s.Profile != "" && !hubapi.Contains([]string{hubapi.ProfileDev, hubapi.ProfileProduction, hubapi.ProfileHighAvailability}, s.Profile) {
			return fmt.Errorf("invalid profile %q", s.Profile)
		}
		if s.CPULimits != "" && !hubapi.Contains([]string{hubapi.CPULimitsKeep, hubapi.CPULimitsRemove, hubapi.CPULimitsRatio}, s.CPULimits) {
			return fmt.Errorf("invalid cpuLimits %q", s.CPULimits)
		}
	}
	return nil
}

func validateEvent(e hubapi.Event) error {
	if _, err := uuid.Parse(e.ID); err != nil {
		return errors.New("id must be a UUID")
	}
	if !hubapi.Contains(hubapi.EventTypes, e.Type) {
		return fmt.Errorf("invalid type %q", e.Type)
	}
	if e.Time.IsZero() {
		return errors.New("time is required")
	}
	if err := validateRef(e.Workload); err != nil {
		return err
	}
	if !reasonRE.MatchString(e.Reason) {
		return fmt.Errorf("reason %q must be CamelCase", e.Reason)
	}
	if e.Strategy != "" && !hubapi.Contains(hubapi.Strategies, e.Strategy) {
		return fmt.Errorf("invalid strategy %q", e.Strategy)
	}
	for _, res := range e.Resources {
		if res != "cpu" && res != "memory" {
			return fmt.Errorf("invalid resource %q", res)
		}
	}
	if e.RecommendationID != "" {
		if _, err := uuid.Parse(e.RecommendationID); err != nil {
			return errors.New("recommendationID must be a UUID")
		}
	}
	if len(e.Message) > 1024 {
		return errors.New("message longer than 1024 characters")
	}
	return nil
}

// ---- request helpers ----

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	b, err := io.ReadAll(body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeProblem(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "Body larger than 8 MiB.")
			return false
		}
		badRequest(w, r, "invalid_body", err.Error())
		return false
	}
	if err := json.Unmarshal(b, v); err != nil {
		badRequest(w, r, "invalid_json", err.Error())
		return false
	}
	return true
}

func intParam(s string, def, minV, maxV int) (int, error) {
	if s == "" {
		return def, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < minV || v > maxV {
		return 0, fmt.Errorf("must be an integer between %d and %d", minV, maxV)
	}
	return v, nil
}

func timeParam(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, fmt.Errorf("invalid RFC 3339 time %q", s)
	}
	return &t, nil
}
