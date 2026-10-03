package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpa"
)

type packageLifecycleRequest struct {
	Package      string `json:"package"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Confirmed    bool   `json:"confirmed,omitempty"`
}

type packageLifecycleResponse struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	GenerationID  string `json:"generation_id,omitempty"`
	Operation     string `json:"operation"`
	Package       string `json:"package"`
	Version       string `json:"version"`
	Architecture  string `json:"architecture"`
	MetadataState string `json:"metadata_state"`
	ArtifactState string `json:"artifact_state"`
	Result        string `json:"result,omitempty"`
	Error         string `json:"error,omitempty"`
}

func (s *server) packageLifecycle(w http.ResponseWriter, r *http.Request, repositoryID, operation string) {
	if r.Method != http.MethodPost {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if operation != string(tpa.LifecycleUnlist) && operation != string(tpa.LifecycleDelete) {
		bad(w, http.StatusNotFound, "not found")
		return
	}
	var request packageLifecycleRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		bad(w, http.StatusBadRequest, "invalid package lifecycle request")
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		bad(w, http.StatusBadRequest, "invalid package lifecycle request")
		return
	}
	if !validLifecycleIdentity(request.Package) || !validLifecycleIdentity(request.Version) || !validLifecycleIdentity(request.Architecture) {
		bad(w, http.StatusBadRequest, "package, version, and architecture must be valid exact identity values")
		return
	}
	if operation == string(tpa.LifecycleDelete) && !request.Confirmed {
		bad(w, http.StatusPreconditionRequired, "confirmed=true is required for permanent artifact deletion")
		return
	}
	if operation == string(tpa.LifecycleUnlist) && request.Confirmed {
		bad(w, http.StatusBadRequest, "confirmed is only valid for delete")
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if !idRE.MatchString(idempotencyKey) {
		bad(w, http.StatusBadRequest, "Idempotency-Key must be a 32-character hexadecimal value")
		return
	}
	actor := accountID(r)
	repository, _, err := s.repositoryPolicy().AuthorizePublication(r.Context(), actor, repositoryID)
	if err != nil {
		if repo, lookupErr := s.db.Repository(r.Context(), repositoryID); lookupErr == nil && repo.Visibility == "private" && repo.AccountID != actor {
			bad(w, http.StatusNotFound, "repository not found")
		} else {
			bad(w, http.StatusForbidden, "package lifecycle operation is not authorized")
		}
		return
	}
	if s.cfg.PublicationBackend != "" && s.cfg.PublicationBackend != "local" {
		bad(w, http.StatusNotImplemented, "package lifecycle operations require the managed local TPA signing backend")
		return
	}
	if s.cfg.GPGFingerprint == "" || s.cfg.TPABin == "" {
		bad(w, http.StatusServiceUnavailable, "managed TPA package lifecycle signing is not configured")
		return
	}

	lock := s.generationLock(repositoryID)
	lock.Lock()
	lockHeld := true
	defer func() {
		if lockHeld {
			lock.Unlock()
		}
	}()
	s.stagingMu.Lock()
	stagingHeld := true
	defer func() {
		if stagingHeld {
			s.stagingMu.Unlock()
		}
	}()
	prior, err := s.db.PackageLifecycleByIdempotency(r.Context(), repositoryID, actor, idempotencyKey)
	if err == nil {
		if prior.Operation != operation || prior.Package != request.Package || prior.Version != request.Version || prior.Architecture != request.Architecture || prior.Confirmed != request.Confirmed {
			bad(w, http.StatusConflict, "Idempotency-Key was already used for a different package lifecycle request")
			return
		}
		job, jobErr := s.db.Job(r.Context(), prior.JobID)
		if jobErr != nil {
			bad(w, http.StatusInternalServerError, "lifecycle job status unavailable")
			return
		}
		status := lifecycleReplayStatus(job, prior)
		lifecycleJSON(w, status, lifecycleResponse(job, prior))
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		bad(w, http.StatusInternalServerError, "lifecycle idempotency lookup failed")
		return
	}
	parent, err := activeGenerationOrEmpty(r.Context(), s.db, repositoryID)
	if err != nil {
		bad(w, http.StatusInternalServerError, "active repository generation unavailable")
		return
	}
	activeRoot, err := s.publishedRepositoryRoot(r.Context(), repositoryID)
	if err != nil {
		bad(w, http.StatusConflict, "active repository generation is unavailable")
		return
	}
	retained, err := s.lifecycleRetainedArtifacts(r.Context(), repositoryID, parent)
	if err != nil {
		bad(w, http.StatusInternalServerError, "lifecycle retention history is unavailable")
		return
	}
	if err = verifyLifecycleCandidate(activeRoot, s.cfg, retained); err != nil {
		bad(w, http.StatusConflict, "active repository failed independent lifecycle verification")
		return
	}
	identity := strings.Join([]string{request.Package, request.Version, request.Architecture}, "\x00")
	artifactPath, artifact, listed, err := findLifecyclePackage(activeRoot, identity, retained)
	if err != nil {
		bad(w, http.StatusConflict, "package identity could not be resolved uniquely")
		return
	}
	if artifactPath == "" {
		bad(w, http.StatusNotFound, "exact package identity not found")
		return
	}
	if operation == string(tpa.LifecycleUnlist) && !listed {
		bad(w, http.StatusConflict, "exact package identity is already unlisted")
		return
	}
	if artifact.size <= 0 || len(artifact.sha256) != 64 {
		bad(w, http.StatusConflict, "package artifact metadata is invalid")
		return
	}
	if err = s.publicationCheckpoint("before-package-lifecycle-job"); err != nil {
		bad(w, http.StatusServiceUnavailable, "package lifecycle admission failed")
		return
	}
	jobID := newID()
	now := s.currentTime().UTC().Format(time.RFC3339Nano)
	incoming := filepath.Join(s.cfg.DataDir, "incoming", "lifecycle-"+jobID)
	job := store.Job{ID: jobID, RepositoryID: repositoryID, PublisherAccountID: actor, State: "running", IncomingPath: incoming, CreatedAt: now, StartedAt: now}
	lifecycle := store.PackageLifecycleJob{JobID: jobID, RepositoryID: repositoryID, ActorAccountID: actor, IdempotencyKey: idempotencyKey, Operation: operation, Package: request.Package, Version: request.Version, Architecture: request.Architecture, Confirmed: request.Confirmed, ArtifactFilename: artifactPath, ArtifactSize: artifact.size, ArtifactSHA256: artifact.sha256, MetadataState: "pending", ArtifactState: "pending"}
	created, wasExisting, err := s.db.CreatePackageLifecycleJob(r.Context(), job, lifecycle, parent, repository.AccountID)
	if err != nil {
		if errors.Is(err, store.ErrRepositoryBusy) || errors.Is(err, store.ErrLifecycleParentChanged) {
			bad(w, http.StatusConflict, "repository generation changed or another mutation is active")
		} else if errors.Is(err, store.ErrLifecycleIdempotencyConflict) {
			bad(w, http.StatusConflict, err.Error())
		} else {
			bad(w, http.StatusInternalServerError, "package lifecycle job could not be recorded")
		}
		return
	}
	if wasExisting {
		job, err = s.db.Job(r.Context(), created.JobID)
		if err != nil {
			bad(w, http.StatusInternalServerError, "lifecycle job status unavailable")
			return
		}
		lifecycleJSON(w, http.StatusAccepted, lifecycleResponse(job, created))
		return
	}
	lifecycle.GenerationBefore = parent
	responseStatus := s.executePackageLifecycle(r.Context(), repository, job, lifecycle, activeRoot, retained, artifact, listed)
	s.stagingMu.Unlock()
	stagingHeld = false
	lock.Unlock()
	lockHeld = false
	finalJob, jobErr := s.db.Job(context.Background(), jobID)
	finalLifecycle, lifecycleErr := s.db.PackageLifecycle(context.Background(), jobID)
	if jobErr != nil || lifecycleErr != nil {
		bad(w, http.StatusInternalServerError, "lifecycle job committed but status lookup failed")
		return
	}
	if finalJob.GenerationID != "" {
		if cleanupErr := s.collectOldGenerations(repositoryID); cleanupErr != nil {
			log.Printf("package lifecycle generation retention: repository=%s: %v", repositoryID, cleanupErr)
			s.signalCleanupRetry()
		}
	}
	lifecycleJSON(w, responseStatus, lifecycleResponse(finalJob, finalLifecycle))
}

func lifecycleReplayStatus(job store.Job, lifecycle store.PackageLifecycleJob) int {
	if job.State == "running" {
		return http.StatusAccepted
	}
	if job.State == "failed" {
		if job.GenerationID != "" && lifecycle.ArtifactState == "cleanup_failed" {
			return http.StatusMultiStatus
		}
		return http.StatusInternalServerError
	}
	return http.StatusOK
}

func lifecycleJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func lifecycleResponse(job store.Job, lifecycle store.PackageLifecycleJob) packageLifecycleResponse {
	return packageLifecycleResponse{ID: job.ID, State: job.State, GenerationID: job.GenerationID, Operation: lifecycle.Operation, Package: lifecycle.Package, Version: lifecycle.Version, Architecture: lifecycle.Architecture, MetadataState: lifecycle.MetadataState, ArtifactState: lifecycle.ArtifactState, Result: lifecycle.Result, Error: job.Error}
}

func validLifecycleIdentity(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 255 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r <= ' ' || r == 0x7f || r == '/' || r == '\\' || r == 0 {
			return false
		}
	}
	return true
}

func (s *server) lifecycleRetainedArtifacts(ctx context.Context, repositoryID, active string) (map[string]candidatePackage, error) {
	history, err := s.db.LifecycleGenerationHistory(ctx, repositoryID, active)
	if err != nil {
		return nil, err
	}
	retained := make(map[string]candidatePackage)
	for i := len(history) - 1; i >= 0; i-- { // apply events oldest to newest.
		entry := history[i]
		identity := strings.Join([]string{entry.Package, entry.Version, entry.Architecture}, "\x00")
		switch entry.ArtifactState {
		case "retained", "cleanup_failed":
			retained[entry.ArtifactFilename] = candidatePackage{candidateChecksum: candidateChecksum{size: entry.ArtifactSize, sha256: entry.ArtifactSHA256}, identity: identity}
		case "deleted":
			delete(retained, entry.ArtifactFilename)
		default:
			return nil, fmt.Errorf("incomplete lifecycle artifact history at %s", entry.JobID)
		}
	}
	return retained, nil
}

func findLifecyclePackage(root, identity string, retained map[string]candidatePackage) (string, candidatePackage, bool, error) {
	component := filepath.Join(root, "dists", "stable", "main")
	binaries, err := os.ReadDir(component)
	if err != nil {
		return "", candidatePackage{}, false, err
	}
	var foundPath string
	var found candidatePackage
	for _, binary := range binaries {
		if !binary.IsDir() || !strings.HasPrefix(binary.Name(), "binary-") {
			continue
		}
		packages := filepath.Join(component, binary.Name(), "Packages")
		matches, readErr := findLifecycleIndexIdentity(root, packages, identity)
		if readErr != nil {
			return "", candidatePackage{}, false, readErr
		}
		for path, artifact := range matches {
			if foundPath != "" && foundPath != path {
				return "", candidatePackage{}, false, fmt.Errorf("identity occurs in multiple package artifacts")
			}
			foundPath, found = path, artifact
		}
	}
	if foundPath != "" {
		return foundPath, found, true, nil
	}
	for path, artifact := range retained {
		if artifact.identity == identity {
			if foundPath != "" {
				return "", candidatePackage{}, false, fmt.Errorf("identity occurs in multiple retained artifacts")
			}
			foundPath, found = path, artifact
		}
	}
	return foundPath, found, false, nil
}

func findLifecycleIndexIdentity(root, packagesPath, identity string) (map[string]candidatePackage, error) {
	file, err := os.Open(packagesPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	matches := map[string]candidatePackage{}
	fields := map[string]string{}
	check := func() error {
		if len(fields) == 0 {
			return nil
		}
		entryIdentity := strings.Join([]string{fields["Package"], fields["Version"], fields["Architecture"]}, "\x00")
		if entryIdentity != identity {
			return nil
		}
		size, parseErr := strconv.ParseInt(fields["Size"], 10, 64)
		if parseErr != nil || size < 0 || len(fields["SHA256"]) != 64 {
			return fmt.Errorf("invalid target package metadata")
		}
		filename := fields["Filename"]
		if _, pathErr := candidateRelativePath(root, filename); pathErr != nil {
			return fmt.Errorf("unsafe package Filename")
		}
		matches[filename] = candidatePackage{candidateChecksum: candidateChecksum{size: size, sha256: strings.ToLower(fields["SHA256"])}, identity: identity}
		return nil
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := check(); err != nil {
				return nil, err
			}
			fields = map[string]string{}
			continue
		}
		if strings.HasPrefix(line, " ") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("malformed Packages index")
		}
		fields[key] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	return matches, nil
}

func (s *server) executePackageLifecycle(ctx context.Context, repository store.Repository, job store.Job, lifecycle store.PackageLifecycleJob, activeRoot string, retained map[string]candidatePackage, artifact candidatePackage, listed bool) int {
	registered := true
	committed := false
	pointerActivated := false
	generationStored := false
	candidate := filepath.Join(job.IncomingPath, "candidate")
	generationRoot := filepath.Join(s.cfg.DataDir, "repos", repository.ID, "generations")
	generationPath := filepath.Join(generationRoot, job.ID)
	pointer := filepath.Join(s.cfg.DataDir, "repos", repository.ID, "current")
	temporaryPointer := pointer + "." + job.ID
	hadPrevious := false
	capacityReserved := false
	var candidateReservation int64
	var candidateInodes int64
	defer func() {
		if committed {
			return
		}
		if pointerActivated {
			if rollbackErr := rollbackLiveSymlink(pointer, temporaryPointer, hadPrevious); rollbackErr != nil {
				panic(fmt.Sprintf("package lifecycle rollback failed: %v", rollbackErr))
			}
			_ = syncDirectory(filepath.Dir(pointer))
			pointerActivated = false
		}
		_ = os.Remove(temporaryPointer)
		if generationStored {
			_ = os.RemoveAll(generationPath)
			_ = syncDirectory(generationRoot)
		}
		cleanupErr := s.cleanupPublishJob(job)
		if registered {
			failure := "package lifecycle operation failed before activation"
			if current, lookupErr := s.db.Job(context.Background(), job.ID); lookupErr == nil && current.Error != "" {
				failure = current.Error
			}
			_ = s.db.FailPackageLifecycle(context.Background(), job.ID, repository.ID, failure, s.currentTime().UTC().Format(time.RFC3339Nano))
			if cleanupErr == nil {
				_ = s.db.SetCleanupResult(context.Background(), job.ID, nil)
			} else {
				_ = s.db.SetCleanupRetry(context.Background(), job.ID, cleanupErr, s.cleanupRetryAt(s.cleanupCurrentTime(), 1))
				s.signalCleanupRetry()
			}
		}
		if capacityReserved {
			s.releaseCapacity(job.ID)
		}
	}()
	if err := mkdirAllDurable(job.IncomingPath, 0700); err != nil {
		return s.failLifecycleResponse(job, err)
	}
	if err := mkdirAllDurable(generationRoot, 0755); err != nil {
		return s.failLifecycleResponse(job, err)
	}
	size, inodeCount, err := lifecycleTreeMetrics(activeRoot)
	if err != nil {
		return s.failLifecycleResponse(job, err)
	}
	if size > (math.MaxInt64-(32<<20))/2 || inodeCount > (1<<20)-512 {
		return s.failLifecycleResponse(job, fmt.Errorf("active repository exceeds bounded lifecycle limits"))
	}
	runnerMaximum := size*2 + (32 << 20)
	runnerInodes := inodeCount*2 + 512
	if runnerInodes < 4096 {
		runnerInodes = 4096
	}
	if size > math.MaxInt64-(16<<20) {
		return s.failLifecycleResponse(job, fmt.Errorf("lifecycle candidate size overflow"))
	}
	candidateBound := size + (16 << 20)
	allocationBlock, allocationErr := filesystemAllocationBlock(s.cfg.DataDir)
	if allocationErr != nil {
		return s.failLifecycleResponse(job, allocationErr)
	}
	candidateInodes = inodeCount + 64
	if candidateInodes < 64 {
		candidateInodes = 64
	}
	if candidateInodes > math.MaxInt64/allocationBlock || candidateBound > math.MaxInt64-candidateInodes*allocationBlock {
		return s.failLifecycleResponse(job, fmt.Errorf("lifecycle reservation overflow"))
	}
	candidateReservation = candidateBound + candidateInodes*allocationBlock
	if err = s.checkDiskHeadroom(); err == nil {
		err = s.reserveCapacityWithResources(context.Background(), repository.AccountID, repository.ID, job.ID, "candidate", candidateReservation, candidateInodes)
	}
	if err != nil {
		return s.failLifecycleResponse(job, err)
	}
	capacityReserved = true
	if err = s.publicationCheckpoint("lifecycle-candidate-start"); err != nil {
		return s.failLifecycleResponse(job, err)
	}
	request := tpa.LifecycleRequest{Operation: tpa.LifecycleOperation(lifecycle.Operation), Input: activeRoot, Output: candidate, Package: lifecycle.Package, Version: lifecycle.Version, Architecture: lifecycle.Architecture, SigningHome: publicationSigningHome(s.cfg), SigningFingerprint: s.cfg.GPGFingerprint}
	timeout := s.cfg.PublishTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	result, runErr := publicationTPARunner(s.cfg).LifecycleBounded(operationCtx, request, runnerMaximum, runnerInodes)
	cancel()
	if runErr != nil {
		return s.failLifecycleResponse(job, runErr)
	}
	if err = s.publicationCheckpoint("lifecycle-candidate-built"); err != nil {
		return s.failLifecycleResponse(job, err)
	}
	candidateOrphans := cloneLifecycleArtifacts(retained)
	metadataState := "unchanged"
	artifactState := "deleted"
	resultName := "artifact_deleted"
	if lifecycle.Operation == string(tpa.LifecycleUnlist) {
		candidateOrphans[lifecycle.ArtifactFilename] = artifact
		metadataState, artifactState, resultName = "published", "retained", "package_unlisted"
	} else if result.CleanupFailed {
		candidateOrphans[lifecycle.ArtifactFilename] = artifact
		metadataState, artifactState, resultName = "published", "cleanup_failed", "unlisted_cleanup_failed"
	} else {
		delete(candidateOrphans, lifecycle.ArtifactFilename)
		if listed {
			metadataState, resultName = "published", "package_deleted"
		} else {
			resultName = "unlisted_artifact_deleted"
		}
	}
	if err = checkCandidateBounds(candidate, candidateBound, candidateReservation); err != nil {
		return s.failLifecycleResponse(job, err)
	}
	if err = s.checkDiskHeadroom(); err == nil {
		err = s.checkStorageCapacity(repository.ID, candidate)
	}
	if err != nil {
		return s.failLifecycleResponse(job, err)
	}
	if err = verifyLifecycleCandidate(candidate, s.cfg, candidateOrphans); err != nil {
		return s.failLifecycleResponse(job, fmt.Errorf("TPA lifecycle candidate failed independent verification: %w", err))
	}
	if err = syncTree(candidate); err != nil {
		return s.failLifecycleResponse(job, err)
	}
	if err = os.Rename(candidate, generationPath); err != nil {
		return s.failLifecycleResponse(job, err)
	}
	generationStored = true
	if err = syncDirectory(generationRoot); err != nil {
		return s.failLifecycleResponse(job, err)
	}
	if err = os.Symlink(filepath.Join("generations", job.ID), temporaryPointer); err != nil {
		return s.failLifecycleResponse(job, err)
	}
	_, previousErr := os.Lstat(pointer)
	hadPrevious = previousErr == nil
	if previousErr != nil && !os.IsNotExist(previousErr) {
		return s.failLifecycleResponse(job, previousErr)
	}
	if err = swapSymlinkPreservingPrevious(temporaryPointer, pointer); err != nil {
		return s.failLifecycleResponse(job, err)
	}
	pointerActivated = true
	if err = syncDirectory(filepath.Dir(pointer)); err != nil {
		return s.failLifecycleResponse(job, err)
	}
	if err = s.publicationCheckpoint("lifecycle-before-authority-commit"); err != nil {
		return s.failLifecycleResponse(job, err)
	}
	finish := s.currentTime().UTC().Format(time.RFC3339Nano)
	if s.durablePublicationFault != nil {
		err = s.durablePublicationFault()
	} else {
		err = s.db.CommitPackageLifecycleActivation(context.Background(), repository.ID, job.ID, repository.AccountID, lifecycle.GenerationBefore, metadataState, artifactState, resultName, map[bool]string{true: "", false: result.Diagnostic}[!result.CleanupFailed], finish, !result.CleanupFailed)
	}
	if err != nil {
		return s.failLifecycleResponse(job, fmt.Errorf("commit lifecycle generation: %w", err))
	}
	committed, registered = true, false
	pointerActivated = false
	_ = os.Remove(temporaryPointer)
	s.releaseCapacity(job.ID)
	capacityReserved = false
	if err = s.cleanupPublishJob(job); err != nil {
		_ = s.db.SetCleanupRetry(context.Background(), job.ID, err, s.cleanupRetryAt(s.cleanupCurrentTime(), 1))
		s.signalCleanupRetry()
	} else {
		_ = s.db.SetCleanupResult(context.Background(), job.ID, nil)
	}
	if err = s.observeAccountStorage(context.Background(), repository.ID, s.currentTime()); err != nil {
		log.Printf("package lifecycle storage accounting: repository=%s job=%s: %v", repository.ID, job.ID, err)
	}
	if err = s.publicationCheckpoint("lifecycle-after-commit"); err != nil {
		log.Printf("package lifecycle post-commit checkpoint: repository=%s job=%s: %v", repository.ID, job.ID, err)
	}
	// The handler owns stagingMu and the repository lock until it serializes the
	// response; retention runs after those locks are released by the caller.
	if result.CleanupFailed {
		return http.StatusMultiStatus
	}
	return http.StatusOK
}

func (s *server) failLifecycleResponse(job store.Job, cause error) int {
	if cause == nil {
		cause = fmt.Errorf("package lifecycle operation failed")
	}
	_ = s.db.SetPackageLifecycleDiagnostic(context.Background(), job.ID, cause.Error())
	log.Printf("package lifecycle failed job=%s: %v", job.ID, cause)
	return http.StatusInternalServerError
}

func cloneLifecycleArtifacts(source map[string]candidatePackage) map[string]candidatePackage {
	copy := make(map[string]candidatePackage, len(source)+1)
	for path, artifact := range source {
		copy[path] = artifact
	}
	return copy
}

func lifecycleTreeMetrics(root string) (int64, int64, error) {
	var size, entries int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != root {
			if entries == math.MaxInt64 {
				return fmt.Errorf("lifecycle tree entry count overflow")
			}
			entries++
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("active repository contains a non-regular file")
		}
		if info.Size() > math.MaxInt64-size {
			return fmt.Errorf("lifecycle tree size overflow")
		}
		size += info.Size()
		return nil
	})
	return size, entries, err
}
