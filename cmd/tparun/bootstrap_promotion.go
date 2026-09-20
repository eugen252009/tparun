package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

type bootstrapPromotion struct {
	RepositoryID string `json:"repository_id"`
	ArtifactPath string `json:"artifact_path"`
	SHA256       string `json:"sha256"`
	Package      string `json:"package"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	PromotedAt   string `json:"promoted_at"`
	Operator     string `json:"operator"`
}

func bootstrapPromotionFile(dataDir string) string {
	return filepath.Join(dataDir, "bootstrap-promotion.json")
}

func (s *server) bootstrapAlias(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	promotion, found, err := s.readBootstrapPromotion()
	if err != nil || !found {
		bad(w, http.StatusNotFound, "bootstrap package unavailable")
		return
	}
	repo, err := s.db.Repository(r.Context(), promotion.RepositoryID)
	if err != nil || repo.Visibility != "public" || repo.Name != "bootstrap" {
		bad(w, http.StatusNotFound, "bootstrap package unavailable")
		return
	}
	view, err := s.repositoryView(r.Context(), repo)
	if err != nil || view.Username != "tparun" {
		bad(w, http.StatusNotFound, "bootstrap package unavailable")
		return
	}
	generation, err := s.db.ActiveGeneration(r.Context(), repo.ID)
	if err != nil {
		bad(w, http.StatusNotFound, "bootstrap package unavailable")
		return
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repo.ID, "generations", generation)
	artifact := filepath.Join(root, filepath.Clean("/"+promotion.ArtifactPath))
	if filepath.Clean(artifact) != artifact {
		bad(w, http.StatusNotFound, "bootstrap package unavailable")
		return
	}
	info, err := os.Stat(artifact)
	if err != nil || !info.Mode().IsRegular() {
		bad(w, http.StatusNotFound, "bootstrap package unavailable")
		return
	}
	location := "/r/" + view.Username + "/" + view.Name + "/" + promotion.ArtifactPath
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, location, http.StatusTemporaryRedirect)
}

func (s *server) readBootstrapPromotion() (bootstrapPromotion, bool, error) {
	data, err := os.ReadFile(bootstrapPromotionFile(s.cfg.DataDir))
	if os.IsNotExist(err) {
		return bootstrapPromotion{}, false, nil
	}
	if err != nil {
		return bootstrapPromotion{}, false, err
	}
	var promotion bootstrapPromotion
	if err := json.Unmarshal(data, &promotion); err != nil {
		return bootstrapPromotion{}, false, fmt.Errorf("decode bootstrap promotion: %w", err)
	}
	return promotion, true, nil
}

func promoteBootstrap(ctx context.Context, db *store.Store, dataDir, repositoryID, artifactPath, operator string, now time.Time) (bootstrapPromotion, error) {
	if repositoryID == "" || operator == "" {
		return bootstrapPromotion{}, fmt.Errorf("repository and operator are required")
	}
	repo, err := db.Repository(ctx, repositoryID)
	if err != nil {
		return bootstrapPromotion{}, fmt.Errorf("find bootstrap repository: %w", err)
	}
	if repo.Visibility != "public" || repo.Name != "bootstrap" {
		return bootstrapPromotion{}, fmt.Errorf("bootstrap promotion requires the public bootstrap repository")
	}
	account, err := db.AccountByID(ctx, repo.AccountID)
	if err != nil || accountUsername(account.Email) != "tparun" {
		return bootstrapPromotion{}, fmt.Errorf("bootstrap promotion requires the canonical tparun/bootstrap owner")
	}
	clean := filepath.Clean("/" + artifactPath)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." || strings.HasPrefix(clean, "../") || !strings.HasPrefix(clean, "pool/main/t/tparun-release/") {
		return bootstrapPromotion{}, fmt.Errorf("artifact is outside the bootstrap package path")
	}
	generation, err := db.ActiveGeneration(ctx, repositoryID)
	if err != nil {
		return bootstrapPromotion{}, fmt.Errorf("find active bootstrap publication: %w", err)
	}
	path := filepath.Join(dataDir, "repos", repositoryID, "generations", generation, clean)
	if filepath.Clean(path) != path {
		return bootstrapPromotion{}, fmt.Errorf("unsafe bootstrap artifact path")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return bootstrapPromotion{}, fmt.Errorf("bootstrap artifact is not published")
	}
	fields := []string{"Package", "Version", "Architecture"}
	values := make([]string, len(fields))
	for i, field := range fields {
		out, commandErr := exec.Command("dpkg-deb", "-f", path, field).Output()
		if commandErr != nil {
			return bootstrapPromotion{}, fmt.Errorf("inspect bootstrap artifact: %w", commandErr)
		}
		values[i] = strings.TrimSpace(string(out))
	}
	if values[0] != "tparun-release" || values[2] != "all" {
		return bootstrapPromotion{}, fmt.Errorf("artifact is not tparun-release architecture all")
	}
	hash := sha256.New()
	file, err := os.Open(path)
	if err != nil {
		return bootstrapPromotion{}, err
	}
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return bootstrapPromotion{}, copyErr
	}
	if closeErr != nil {
		return bootstrapPromotion{}, closeErr
	}
	promotion := bootstrapPromotion{RepositoryID: repositoryID, ArtifactPath: clean, SHA256: hex.EncodeToString(hash.Sum(nil)), Package: values[0], Version: values[1], Architecture: values[2], PromotedAt: now.UTC().Format(time.RFC3339Nano), Operator: operator}
	data, err := json.MarshalIndent(promotion, "", "  ")
	if err != nil {
		return bootstrapPromotion{}, err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dataDir, ".bootstrap-promotion-")
	if err != nil {
		return bootstrapPromotion{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return bootstrapPromotion{}, err
	}
	if err = os.Rename(tmpName, bootstrapPromotionFile(dataDir)); err != nil {
		return bootstrapPromotion{}, err
	}
	if err = db.RecordBootstrapPromotion(ctx, repo.AccountID, repo.ID, operator, promotion.ArtifactPath, promotion.SHA256, promotion.Version, promotion.PromotedAt); err != nil {
		return bootstrapPromotion{}, err
	}
	return promotion, nil
}
