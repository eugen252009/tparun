package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

func (s *server) acceptPublicationSessionBatch(ctx context.Context, sessionID, accountID, repositoryID string, batch int64, tempPath string, artifacts []store.PublicationArtifact, createdAt string) (bool, error) {
	session, err := s.db.PublicationSession(ctx, sessionID)
	if err != nil {
		return false, err
	}
	wantRoot := filepath.Join(s.cfg.DataDir, "incoming", session.ID)
	if session.State != "open" || session.OwnerAccountID != accountID || session.RepositoryID != repositoryID || filepath.Clean(session.IncomingPath) != wantRoot {
		return false, fmt.Errorf("publication session is not open for this account and repository")
	}
	digest := sessionBatchDigest(artifacts)
	replay, err := s.db.PublicationSessionBatchReplay(ctx, sessionID, accountID, batch, digest)
	if err != nil {
		return false, err
	}
	if replay {
		if err = s.removePath(tempPath); err != nil {
			return false, err
		}
		return true, nil
	}
	batchRoot := filepath.Join(wantRoot, "batches")
	if err = os.MkdirAll(batchRoot, 0700); err != nil {
		return false, err
	}
	destination := filepath.Join(batchRoot, fmt.Sprintf("batch-%08d", batch))
	if _, err = os.Lstat(destination); err == nil {
		// A previous process may have died after moving bytes but before
		// committing the batch row. Such bytes are not accepted session state.
		if err = s.removePath(destination); err != nil {
			return false, err
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err = os.Rename(tempPath, destination); err != nil {
		return false, err
	}
	if err = s.db.RecordPublicationSessionBatch(ctx, sessionID, accountID, batch, digest, createdAt, artifacts); err != nil {
		_ = s.removePath(destination)
		return false, err
	}
	return false, nil
}

type publicationSessionStart struct {
	SessionID string                  `json:"session_id"`
	BatchSize int                     `json:"batch_size"`
	Plan      publicationPlanResponse `json:"plan"`
}

func (s *server) createPublicationSession(w http.ResponseWriter, r *http.Request, repositoryID string) {
	if r.Method != http.MethodPost {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	openSessions, err := s.db.OpenPublicationSessions(r.Context())
	if err != nil {
		bad(w, http.StatusInternalServerError, "could not inspect publication sessions")
		return
	}
	for _, open := range openSessions {
		if open.OwnerAccountID == accountID(r) {
			bad(w, http.StatusConflict, fmt.Sprintf("publication session %s is still open; abort it with DELETE /v1/publication-sessions/%s", open.ID, open.ID))
			return
		}
	}
	var inputs []publicationPlanInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&inputs); err != nil {
		bad(w, http.StatusBadRequest, "invalid publication session manifest")
		return
	}
	plan, err := s.publicationPlan(r.Context(), accountID(r), repositoryID, inputs)
	if err != nil {
		bad(w, http.StatusForbidden, err.Error())
		return
	}
	if !plan.Allowed {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		jsonOut(w, publicationSessionStart{BatchSize: maxUploadFiles, Plan: plan})
		return
	}
	id := newID()
	incoming := filepath.Join(s.cfg.DataDir, "incoming", id)
	expected := make([]store.PublicationArtifact, 0, len(inputs))
	for _, input := range inputs {
		expected = append(expected, store.PublicationArtifact{Filename: input.Filename, Package: input.Package, Version: input.Version, Architecture: input.Architecture, UploadedSize: input.Size, UploadedSHA256: strings.ToLower(input.SHA256)})
	}
	now := s.currentTime().UTC().Format(time.RFC3339Nano)
	session := store.PublicationSession{ID: id, RepositoryID: repositoryID, OwnerAccountID: accountID(r), IncomingPath: incoming, State: "open", ExpectedCount: int64(len(expected)), CreatedAt: now}
	if err = s.db.CreatePublicationSession(r.Context(), session, expected); err != nil {
		bad(w, http.StatusBadRequest, "could not create publication session: "+err.Error())
		return
	}
	jsonOut(w, publicationSessionStart{SessionID: id, BatchSize: maxUploadFiles, Plan: plan})
}

func sessionBatchDigest(artifacts []store.PublicationArtifact) string {
	hash := sha256.New()
	for _, artifact := range artifacts {
		fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00%d\x00%s\n", artifact.Filename, artifact.Package, artifact.Version, artifact.Architecture, artifact.UploadedSize, strings.ToLower(artifact.UploadedSHA256))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *server) publicationSessionRoute(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/publication-sessions/"), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || !idRE.MatchString(parts[0]) || (len(parts) > 2) {
		bad(w, http.StatusNotFound, "publication session not found")
		return
	}
	session, err := s.db.PublicationSession(r.Context(), parts[0])
	if err != nil || session.OwnerAccountID != accountID(r) {
		bad(w, http.StatusNotFound, "publication session not found")
		return
	}
	if len(parts) == 2 && parts[1] == "finalize" && r.Method == http.MethodPost {
		if _, _, err = s.repositoryPolicy().AuthorizePublication(r.Context(), accountID(r), session.RepositoryID); err != nil {
			bad(w, http.StatusForbidden, err.Error())
			return
		}
		s.stagingMu.Lock()
		defer s.stagingMu.Unlock()
		session, err = s.db.PublicationSession(r.Context(), parts[0])
		if err != nil || session.OwnerAccountID != accountID(r) {
			bad(w, http.StatusNotFound, "publication session not found")
			return
		}
		if session.State == "open" {
			err = s.db.FinalizePublicationSession(r.Context(), session.ID, accountID(r), s.currentTime().UTC().Format(time.RFC3339Nano))
			if err != nil {
				bad(w, http.StatusConflict, err.Error())
				return
			}
		} else if session.State != "finalized" {
			bad(w, http.StatusConflict, "publication session is not finalizable")
			return
		}
		w.WriteHeader(http.StatusAccepted)
		jsonOut(w, map[string]string{"id": session.ID, "state": "queued"})
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		s.stagingMu.Lock()
		defer s.stagingMu.Unlock()
		session, err = s.db.PublicationSession(r.Context(), parts[0])
		if err != nil || session.OwnerAccountID != accountID(r) {
			bad(w, http.StatusNotFound, "publication session not found")
			return
		}
		if session.State != "open" {
			bad(w, http.StatusConflict, "publication session is no longer open")
			return
		}
		if filepath.Clean(session.IncomingPath) != filepath.Join(s.cfg.DataDir, "incoming", session.ID) {
			bad(w, http.StatusInternalServerError, "publication session path is invalid")
			return
		}
		if err = s.removePath(session.IncomingPath); err != nil {
			bad(w, http.StatusInternalServerError, "publication session cleanup failed")
			return
		}
		if err = s.db.DeleteOpenPublicationSession(r.Context(), session.ID); err != nil {
			bad(w, http.StatusConflict, "publication session could not be aborted")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	bad(w, http.StatusNotFound, "publication session route not found")
}
