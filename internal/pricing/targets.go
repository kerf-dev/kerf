package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// HubTargets lists the clusters with a metrics binding from the hub (GET
// /v1/internal/metrics-bindings, pricing token) and queries their datasources through the hub's
// query proxy, which adds the datasource's credentials (D-026). Clusters without a binding are
// read from --vm-url as before D-025.
type HubTargets struct {
	// HubURL is the hub API base URL, e.g. http://hub-api.hub.svc:8080.
	HubURL string
	// Token returns the pricing token (re-read on every request).
	Token func() (string, error)
	HTTP  *http.Client
}

// FileToken reads a token file on every call (a mounted Secret; rotation needs no restart).
func FileToken(path string) func() (string, error) {
	return func() (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
}

func (h *HubTargets) client() *http.Client {
	hc := h.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &http.Client{Timeout: hc.Timeout, Transport: &bearer{next: hc.Transport, token: h.Token}}
}

// bearer adds the pricing token to every request.
type bearer struct {
	next  http.RoundTripper
	token func() (string, error)
}

func (b *bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	next := b.next
	if next == nil {
		next = http.DefaultTransport
	}
	if b.token != nil {
		tok, err := b.token()
		if err != nil {
			return nil, fmt.Errorf("pricing token: %w", err)
		}
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	return next.RoundTrip(r)
}

// Targets implements TargetLister.
func (h *HubTargets) Targets(ctx context.Context) ([]Target, error) {
	base := strings.TrimRight(h.HubURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/internal/metrics-bindings", nil)
	if err != nil {
		return nil, err
	}
	hc := h.client()
	res, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hub metrics bindings: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub metrics bindings: HTTP %d: %s", res.StatusCode, truncate(string(body), 300))
	}
	var list hubapi.MetricsBindingList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("hub metrics bindings: %w", err)
	}
	var out []Target
	for _, it := range list.Items {
		if !it.Bound {
			continue
		}
		b := it.Binding
		m, err := datasource.NewMatcher(it.Cluster, &b)
		if err != nil {
			return nil, fmt.Errorf("cluster %s: %w", it.Cluster, err)
		}
		ds := b.DatasourceID
		if ds == "" {
			ds = hubapi.LocalDatasourceID
		}
		out = append(out, Target{Cluster: it.Cluster, BaseURL: base + "/v1/internal/datasources/" + url.PathEscape(ds), HTTP: hc, Matcher: m})
	}
	return out, nil
}
