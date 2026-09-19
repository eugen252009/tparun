package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestStagedPublicationClearsAfterAcceptedSuccess(t *testing.T) {
	packagePath := filepath.Join(t.TempDir(), "staged_1_amd64.deb")
	buildSemanticTestDebForArchitecture(t, packagePath, "amd64")
	t.Setenv("TPAHUB_STAGING_DIR", filepath.Join(t.TempDir(), "staging"))
	if err := stageCLI([]string{packagePath}); err != nil {
		t.Fatal(err)
	}
	const repositoryID = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/repositories":
			json.NewEncoder(w).Encode([]map[string]string{{"id": repositoryID, "name": "staged"}})
		case "/v1/repositories/" + repositoryID + "/publication-plan":
			json.NewEncoder(w).Encode(publicationPlanResponse{RepositoryID: repositoryID, CurrentPackageCount: 0, StagedPackageCount: 1, StagedStorageBytes: 1, ProjectedPackageCount: 1, ProjectedStorageBytes: 1, ProjectedAccountStorageBytes: 1, StorageLimitBytes: 100, RemainingStorageBytes: 99, Allowed: true, Items: []publicationPlanItem{{Classification: "NEW", Package: "test", Version: "1", Architecture: "amd64"}}})
		case "/v1/repositories/" + repositoryID + "/publishes":
			json.NewEncoder(w).Encode(map[string]string{"id": "job-1", "state": "queued"})
		case "/v1/publish-jobs/job-1":
			json.NewEncoder(w).Encode(map[string]string{"id": "job-1", "state": "succeeded"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if err := publishStaged(cliConfig{Server: server.URL, Token: "token"}, repositoryID, true); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadStagingManifest()
	if err != nil || len(manifest.Artifacts) != 0 {
		t.Fatalf("successful publication did not clear staging: %+v err=%v", manifest, err)
	}
}

func TestStagedPublicationRunsRealServerPublication(t *testing.T) {
	s, h := testServer(t)
	s.ctx = context.Background()
	s.cfg.PublishTimeout = time.Minute
	s.runner = copyingPublicationRunner{}
	const repositoryID = "efefefefefefefefefefefefefefefef"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "staged-real", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.cfg.DataDir, "repos", repositoryID), 0755); err != nil {
		t.Fatal(err)
	}
	packagePath := filepath.Join(t.TempDir(), "staged-real_1_amd64.deb")
	buildSemanticTestDebForArchitecture(t, packagePath, "amd64")
	t.Setenv("TPAHUB_STAGING_DIR", filepath.Join(t.TempDir(), "staging"))
	if err := stageCLI([]string{packagePath}); err != nil {
		t.Fatal(err)
	}
	processed := make(chan struct{})
	go func() {
		defer close(processed)
		for i := 0; i < 200; i++ {
			job, err := s.db.Claim(context.Background())
			if err == nil {
				s.process(job)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if err := publishStaged(cliConfig{Server: h.URL, Token: "tph_s_test"}, repositoryID, true); err != nil {
		t.Fatal(err)
	}
	<-processed
	manifest, err := loadStagingManifest()
	if err != nil || len(manifest.Artifacts) != 0 {
		t.Fatalf("real publication did not clear staging: %+v err=%v", manifest, err)
	}
}
