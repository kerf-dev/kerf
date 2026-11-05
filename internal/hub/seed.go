package hub

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
)

// DevSeedTokens reads the dev tokens: files in TokensDir (one per key) or the inline map.
// Keys are cluster names (agent tokens), "ui" and "admin".
func DevSeedTokens(d DevSeed) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range d.Tokens {
		out[k] = strings.TrimSpace(v)
	}
	if d.TokensDir == "" {
		return out, nil
	}
	entries, err := os.ReadDir(d.TokensDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		// Secret volumes contain ..data symlinks; take regular names only.
		if strings.HasPrefix(e.Name(), ".") || e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(d.TokensDir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = strings.TrimSpace(string(b))
	}
	return out, nil
}

// RunDevSeed registers the dev clusters and stores the hashes of the dev tokens. Idempotent.
func RunDevSeed(ctx context.Context, st *store.Store, d DevSeed, log *slog.Logger) error {
	for _, c := range d.Clusters {
		if !hubapi.ClusterNameRE.MatchString(c) {
			return fmt.Errorf("devSeed: invalid cluster name %q", c)
		}
		if err := st.EnsureCluster(ctx, c, "registered by dev seed"); err != nil {
			return err
		}
	}
	tokens, err := DevSeedTokens(d)
	if err != nil {
		return err
	}
	for key, tok := range tokens {
		if tok == "" {
			continue
		}
		role, cluster := hubapi.RoleAgent, key
		switch key {
		case hubapi.RoleUI, hubapi.RoleAdmin, hubapi.RolePricing:
			role, cluster = key, ""
		default:
			if err := st.EnsureCluster(ctx, key, "registered by dev seed"); err != nil {
				return err
			}
		}
		if err := st.EnsureToken(ctx, tok, role, cluster, "dev seed: "+key); err != nil {
			return err
		}
	}
	log.InfoContext(ctx, "dev seed applied", "clusters", len(d.Clusters), "tokens", len(tokens))
	return nil
}
