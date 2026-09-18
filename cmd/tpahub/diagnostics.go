package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
	appversion "github.com/eugen252009/tpahub/internal/version"
)

func (s *server) ready(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.lifecycle != nil && s.lifecycle.draining.Load() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ready(ctx); err != nil {
		http.Error(w, "persistent state unavailable", http.StatusServiceUnavailable)
		return
	}
	jsonOut(w, map[string]any{"ready": true, "schema": store.CurrentSchemaVersion()})
}

// Local-only diagnostics: explicit allowlist, never serialize the full config.
func diagnostics() error {
	cfg, err := config.LoadServer()
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"version": appversion.Current, "revision": appversion.BuildRevision, "schema": store.CurrentSchemaVersion(),
		"data_directory": dir, "repository_directory": filepath.Join(dir, "repos"),
		"delivery": cfg.DeliveryMode, "enforcement": cfg.EgressEnforcement,
		"billing": cfg.BillingIntegration, "capability_source": cfg.CapabilitySource,
		"listen": cfg.Listen, "tpa_binary": cfg.TPABin,
		"signing_configured": cfg.GPGFingerprint != "",
	})
}
