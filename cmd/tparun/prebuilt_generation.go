package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
)

func (s *server) uploadPrebuiltGeneration(w http.ResponseWriter, r *http.Request, repositoryID string) {
	if r.Method != http.MethodPost {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, _, err := s.repositoryPolicy().AuthorizePublication(r.Context(), accountID(r), repositoryID); err != nil {
		bad(w, http.StatusForbidden, err.Error())
		return
	}
	repository, err := s.db.Repository(r.Context(), repositoryID)
	if err != nil {
		bad(w, http.StatusNotFound, "repository not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload)
	reader, err := r.MultipartReader()
	if err != nil {
		bad(w, http.StatusBadRequest, "multipart generation upload required")
		return
	}
	var sessionBytes [16]byte
	if _, err = rand.Read(sessionBytes[:]); err != nil {
		bad(w, http.StatusInternalServerError, "publication identity unavailable")
		return
	}
	publicationID := hex.EncodeToString(sessionBytes[:])
	if err = s.db.ClaimRepositoryPublication(r.Context(), repositoryID, publicationID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		if err == store.ErrRepositoryBusy {
			bad(w, http.StatusConflict, "repository publication already in progress")
		} else {
			bad(w, http.StatusInternalServerError, "repository publication claim failed")
		}
		return
	}

	incoming := ""
	generationPath := ""
	generationStored, pointerActivated, committed := false, false, false
	reservationOwned, slotOwned := false, true
	defer func() {
		if !committed {
			s.stagingMu.Lock()
			if incoming != "" {
				_ = os.RemoveAll(incoming)
			}
			if generationStored && !pointerActivated && generationPath != "" {
				if removeErr := s.removePath(generationPath); removeErr != nil {
					log.Printf("failed prebuilt generation cleanup: publication=%s: %v", publicationID, removeErr)
				} else if syncErr := syncDirectory(filepath.Dir(generationPath)); syncErr != nil {
					log.Printf("failed prebuilt generation cleanup sync: publication=%s: %v", publicationID, syncErr)
				}
			}
			if reservationOwned {
				s.releaseCapacity(publicationID)
			}
			s.stagingMu.Unlock()
		}
		if slotOwned {
			if releaseErr := s.db.ReleaseRepositoryPublication(context.Background(), repositoryID, publicationID); releaseErr != nil {
				log.Printf("failed repository publication claim release: repository=%s publication=%s: %v", repositoryID, publicationID, releaseErr)
			}
		}
	}()

	first, err := reader.NextPart()
	if err != nil || first.FormName() != "manifest" || first.FileName() != "" {
		bad(w, http.StatusBadRequest, "first multipart part must be the generation manifest")
		return
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(first, maxGenerationManifestBytes+1))
	if err != nil {
		bad(w, http.StatusBadRequest, "generation manifest read failed")
		return
	}
	if len(manifestBytes) > maxGenerationManifestBytes {
		bad(w, http.StatusRequestEntityTooLarge, "generation manifest exceeds its size limit")
		return
	}
	_ = first.Close()
	manifest, declaredBytes, err := decodeGenerationManifest(manifestBytes, repositoryID)
	if err != nil {
		bad(w, http.StatusBadRequest, err.Error())
		return
	}
	if declaredBytes > s.cfg.MaxUpload {
		bad(w, http.StatusRequestEntityTooLarge, "generation exceeds upload limit")
		return
	}
	var inReleaseManifest generationManifestFile
	for _, file := range manifest.Files {
		if file.Path == "dists/stable/InRelease" {
			inReleaseManifest = file
			break
		}
	}
	if inReleaseManifest.Path == "" || inReleaseManifest.Size == 0 {
		bad(w, http.StatusBadRequest, "generation manifest must include a non-empty dists/stable/InRelease")
		return
	}
	current, err := activeGenerationOrEmpty(r.Context(), s.db, repositoryID)
	if err != nil || current != manifest.ParentGeneration {
		bad(w, http.StatusConflict, "expected parent generation does not match current generation")
		return
	}

	allocationBlock, err := filesystemAllocationBlock(s.cfg.DataDir)
	if err != nil {
		bad(w, http.StatusInsufficientStorage, "upload allocation unavailable")
		return
	}
	directoryCount := int64(generationDirectoryCount(manifest))
	reservation, overflow := addCapacity(declaredBytes, int64(len(manifest.Files))*allocationBlock, (directoryCount+2)*allocationBlock)
	if overflow {
		bad(w, http.StatusInsufficientStorage, "generation reservation overflow")
		return
	}
	inodeReservation := int64(len(manifest.Files)) + directoryCount + 128
	incoming = filepath.Join(s.cfg.DataDir, "incoming", publicationID)
	repositoryRoot := filepath.Join(s.cfg.DataDir, "repos", repositoryID)
	generationPath = filepath.Join(repositoryRoot, "generations", manifest.GenerationID)
	s.stagingMu.Lock()
	if err = s.checkDiskHeadroomFor(reservation); err == nil {
		err = s.reserveCapacityWithResources(r.Context(), accountID(r), repositoryID, publicationID, "prebuilt-generation", reservation, inodeReservation)
		reservationOwned = err == nil
	}
	if err == nil {
		if mkdirErr := os.MkdirAll(filepath.Dir(incoming), 0700); mkdirErr != nil {
			err = mkdirErr
		} else if _, statErr := os.Lstat(incoming); statErr == nil {
			err = fmt.Errorf("publication staging identity already exists")
		} else if !os.IsNotExist(statErr) {
			err = statErr
		} else {
			err = os.Mkdir(incoming, 0700)
		}
	}
	s.stagingMu.Unlock()
	if err != nil {
		bad(w, http.StatusInsufficientStorage, err.Error())
		return
	}

	declared := make(map[string]generationManifestFile, len(manifest.Files))
	for _, file := range manifest.Files {
		declared[file.Path] = file
	}
	received := make(map[string]bool, len(manifest.Files))
	// InRelease is the admission part: verify its manifest digest and registered
	// signer before asking multipart.Reader for any large payload part.
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file:dists/stable/InRelease" || part.FileName() != "" {
		if part != nil {
			_ = part.Close()
		}
		bad(w, http.StatusBadRequest, "dists/stable/InRelease must be the first file part")
		return
	}
	inReleasePath := filepath.Join(incoming, "dists", "stable", "InRelease")
	if err = os.MkdirAll(filepath.Dir(inReleasePath), 0700); err != nil {
		_ = part.Close()
		bad(w, http.StatusInsufficientStorage, "generation directory allocation failed")
		return
	}
	n, copyErr := s.copyUploadFile(inReleasePath, part, inReleaseManifest.Size+1)
	if copyErr != nil || n != inReleaseManifest.Size {
		// Do not close/drain a malformed oversized part: reject without reading
		// the attacker-controlled remainder of this field or later file parts.
		bad(w, http.StatusBadRequest, "InRelease file size mismatch")
		return
	}
	_ = part.Close()
	if err = verifyCandidateFile(r.Context(), inReleasePath, inReleaseManifest.Size, inReleaseManifest.SHA256); err != nil {
		bad(w, http.StatusBadRequest, "InRelease manifest verification failed: "+err.Error())
		return
	}
	trusted, err := s.db.RepositoryAPTSigners(r.Context(), repositoryID)
	if err != nil {
		bad(w, http.StatusInternalServerError, "repository signer lookup failed")
		return
	}
	if len(trusted) == 0 {
		bad(w, http.StatusForbidden, "repository has no registered APT signer")
		return
	}
	fingerprint, err := verifyClientInReleaseSigner(r.Context(), inReleasePath, trusted, s.cfg.PublishTimeout)
	if err != nil {
		bad(w, http.StatusBadRequest, "InRelease signer verification failed: "+err.Error())
		return
	}
	received[inReleaseManifest.Path] = true
	uploadedBytes := n
	for {
		part, err = reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			bad(w, http.StatusBadRequest, "generation upload interrupted or malformed")
			return
		}
		fieldName := part.FormName()
		if !strings.HasPrefix(fieldName, "file:") || part.FileName() != "" {
			_ = part.Close()
			bad(w, http.StatusBadRequest, "unexpected multipart field")
			return
		}
		path := strings.TrimPrefix(fieldName, "file:")
		want, ok := declared[path]
		if !ok || received[path] {
			_ = part.Close()
			bad(w, http.StatusBadRequest, "generation contains undeclared or duplicate file")
			return
		}
		received[path] = true
		filePath := filepath.Join(incoming, filepath.FromSlash(path))
		if err = os.MkdirAll(filepath.Dir(filePath), 0700); err != nil {
			_ = part.Close()
			bad(w, http.StatusInsufficientStorage, "generation directory allocation failed")
			return
		}
		n, copyErr = s.copyUploadFile(filePath, part, want.Size+1)
		if copyErr != nil || n != want.Size {
			bad(w, http.StatusBadRequest, "generation file size mismatch")
			return
		}
		_ = part.Close()
		uploadedBytes += n
		if uploadedBytes > declaredBytes {
			bad(w, http.StatusRequestEntityTooLarge, "generation exceeds declared size")
			return
		}
	}
	if len(received) != len(declared) || uploadedBytes != declaredBytes {
		bad(w, http.StatusBadRequest, "generation upload is incomplete")
		return
	}
	if err = verifyGenerationInventoryContext(r.Context(), incoming, manifest); err != nil {
		bad(w, http.StatusBadRequest, "generation inventory verification failed: "+err.Error())
		return
	}
	trusted, err = s.db.RepositoryAPTSigners(r.Context(), repositoryID)
	if err != nil || len(trusted) == 0 {
		bad(w, http.StatusForbidden, "repository has no registered APT signer")
		return
	}
	fingerprint, err = verifyClientSignedGeneration(r.Context(), incoming, trusted, s.cfg.PublishTimeout)
	if err != nil {
		bad(w, http.StatusBadRequest, "signed repository verification failed: "+err.Error())
		return
	}

	// This read/write lock also serializes signer revocation and generation
	// retention, while leaving uploads and verification for other repositories
	// independent. stagingMu is retained only for shared capacity accounting and
	// the short quota/activation transaction below.
	repositoryLock := s.generationLock(repositoryID)
	repositoryLock.Lock()
	repositoryLockHeld := true
	defer func() {
		if repositoryLockHeld {
			repositoryLock.Unlock()
		}
	}()
	if _, _, err = s.repositoryPolicy().AuthorizePublication(r.Context(), accountID(r), repositoryID); err != nil {
		bad(w, http.StatusForbidden, "publication authorization changed")
		return
	}
	trusted, err = s.db.RepositoryAPTSigners(r.Context(), repositoryID)
	if err != nil {
		bad(w, http.StatusInternalServerError, "repository signer lookup failed")
		return
	}
	stillTrusted := false
	for _, signer := range trusted {
		if strings.EqualFold(signer.Fingerprint, fingerprint) {
			stillTrusted = true
			break
		}
	}
	if !stillTrusted {
		bad(w, http.StatusForbidden, "APT signer was revoked during upload")
		return
	}
	current, err = activeGenerationOrEmpty(r.Context(), s.db, repositoryID)
	if err != nil || current != manifest.ParentGeneration {
		bad(w, http.StatusConflict, "expected parent generation changed during upload")
		return
	}
	if err = preparePrebuiltGeneration(incoming); err != nil {
		bad(w, http.StatusBadRequest, "generation permissions invalid")
		return
	}
	if err = syncTree(incoming); err != nil {
		bad(w, http.StatusInternalServerError, "generation data sync failed")
		return
	}
	manifestBytes, err = json.Marshal(manifest)
	if err != nil {
		bad(w, http.StatusBadRequest, "generation manifest encoding failed")
		return
	}
	manifestBytes = append(manifestBytes, '\n')
	now := time.Now().UTC().Format(time.RFC3339Nano)
	jobID := manifest.GenerationID
	job := store.Job{ID: jobID, RepositoryID: repositoryID, PublisherAccountID: accountID(r), State: "running", IncomingPath: incoming, CreatedAt: now}
	if err = s.db.RegisterPrebuiltPublication(r.Context(), job, manifest.ParentGeneration, fingerprint, manifestBytes); err != nil {
		bad(w, http.StatusConflict, "generation publication could not be recorded")
		return
	}
	generations := filepath.Join(repositoryRoot, "generations")
	if err = mkdirAllDurable(generations, 0755); err != nil {
		s.fail(job, err)
		bad(w, http.StatusInternalServerError, "generation storage unavailable")
		return
	}
	if _, err = os.Lstat(generationPath); err == nil {
		s.fail(job, fmt.Errorf("generation identity already exists"))
		bad(w, http.StatusConflict, "generation identity already exists")
		return
	} else if !os.IsNotExist(err) {
		s.fail(job, err)
		bad(w, http.StatusInternalServerError, "generation storage state unavailable")
		return
	}
	// Shared physical and account storage quotas require this settlement lock.
	// Network transfer, inventory walks, chmod, and tree syncing stay outside it.
	s.stagingMu.Lock()
	stagingLockHeld := true
	defer func() {
		if stagingLockHeld {
			s.stagingMu.Unlock()
		}
	}()
	if _, _, err = s.repositoryPolicy().AuthorizePublication(r.Context(), accountID(r), repositoryID); err != nil {
		s.fail(job, err)
		bad(w, http.StatusForbidden, "publication authorization changed")
		return
	}
	current, err = activeGenerationOrEmpty(r.Context(), s.db, repositoryID)
	if err != nil || current != manifest.ParentGeneration {
		if err == nil {
			err = fmt.Errorf("expected parent generation changed during upload")
		}
		s.fail(job, err)
		bad(w, http.StatusConflict, "expected parent generation changed during upload")
		return
	}
	if err = s.checkDiskHeadroom(); err != nil {
		s.fail(job, err)
		bad(w, http.StatusInsufficientStorage, err.Error())
		return
	}
	if err = s.checkStorageCapacity(repositoryID, incoming); err != nil {
		s.fail(job, err)
		bad(w, http.StatusInsufficientStorage, err.Error())
		return
	}
	if err = os.Rename(incoming, generationPath); err != nil {
		s.fail(job, err)
		bad(w, http.StatusInternalServerError, "generation storage failed")
		return
	}
	generationStored = true
	if err = syncDirectory(filepath.Dir(incoming)); err != nil {
		s.fail(job, err)
		bad(w, http.StatusInternalServerError, "generation upload directory sync failed")
		return
	}
	if err = syncDirectory(generations); err != nil {
		s.fail(job, err)
		bad(w, http.StatusInternalServerError, "generation storage sync failed")
		return
	}
	pointer := filepath.Join(repositoryRoot, "current")
	temporary := pointer + "." + jobID
	if err = os.Symlink(filepath.Join("generations", jobID), temporary); err != nil {
		s.fail(job, err)
		bad(w, http.StatusInternalServerError, "generation activation preparation failed")
		return
	}
	_, previousErr := os.Lstat(pointer)
	hadPrevious := previousErr == nil
	if previousErr != nil && !os.IsNotExist(previousErr) {
		_ = os.Remove(temporary)
		s.fail(job, previousErr)
		bad(w, http.StatusInternalServerError, "generation activation state unavailable")
		return
	}
	if err = swapSymlinkPreservingPrevious(temporary, pointer); err != nil {
		_ = os.Remove(temporary)
		s.fail(job, err)
		bad(w, http.StatusInternalServerError, "generation activation failed")
		return
	}
	pointerActivated = true
	rollback := func(cause error) {
		rollbackErr := rollbackLiveSymlink(pointer, temporary, hadPrevious)
		if rollbackErr == nil {
			rollbackErr = syncDirectory(filepath.Dir(pointer))
		}
		if rollbackErr != nil {
			combined := fmt.Errorf("activation failed: %v; pointer rollback failed: %w", cause, rollbackErr)
			s.fail(job, combined)
			if recoveryErr := s.reconcilePublicationState(); recoveryErr != nil {
				panic(fmt.Sprintf("prebuilt generation recovery failed: cause=%v recovery=%v", combined, recoveryErr))
			}
			pointerActivated = false
			return
		}
		pointerActivated = false
		s.fail(job, cause)
	}
	if err = syncDirectory(filepath.Dir(pointer)); err != nil {
		rollback(err)
		bad(w, http.StatusInternalServerError, "generation activation sync failed")
		return
	}
	commitTime := time.Now().UTC().Format(time.RFC3339Nano)
	if s.durablePublicationFault != nil {
		err = s.durablePublicationFault()
	} else {
		err = s.db.CommitAuthorizedPublication(context.Background(), store.AuthorizedPublication{RepositoryID: repositoryID, JobID: jobID, OwnerAccountID: repository.AccountID, ExpectedParent: manifest.ParentGeneration, CheckParent: true, PublicationID: publicationID}, commitTime, commitTime)
	}
	if err != nil {
		rollback(err)
		active, activeErr := activeGenerationOrEmpty(context.Background(), s.db, repositoryID)
		if activeErr == nil && active != manifest.ParentGeneration {
			bad(w, http.StatusConflict, "generation parent changed before activation")
		} else {
			bad(w, http.StatusInternalServerError, "generation activation record failed")
		}
		return
	}
	slotOwned = false // the activation transaction released the exact claim
	_ = os.Remove(temporary)
	committed = true
	if err = s.db.SetCleanupResult(context.Background(), jobID, nil); err != nil {
		log.Printf("prebuilt publication cleanup state: job=%s: %v", jobID, err)
	}
	s.releaseCapacity(publicationID)
	reservationOwned = false
	if err = s.observeAccountStorage(context.Background(), repositoryID, time.Now()); err != nil {
		log.Printf("prebuilt publication storage accounting: repository=%s: %v", repositoryID, err)
	}
	s.stagingMu.Unlock()
	stagingLockHeld = false
	repositoryLock.Unlock()
	repositoryLockHeld = false
	if err = s.collectOldGenerations(repositoryID); err != nil {
		log.Printf("prebuilt generation retention: repository=%s: %v", repositoryID, err)
		s.signalCleanupRetry()
	}
	w.WriteHeader(http.StatusAccepted)
	jsonOut(w, map[string]string{"id": jobID, "state": "succeeded", "generation_id": jobID, "apt_signer": fingerprint})
}

func activeGenerationOrEmpty(ctx context.Context, db *store.Store, repositoryID string) (string, error) {
	generation, err := db.ActiveGeneration(ctx, repositoryID)
	if err == store.ErrNotFound {
		return "", nil
	}
	return generation, err
}

func preparePrebuiltGeneration(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("generation contains symlink")
		}
		if entry.IsDir() {
			return os.Chmod(path, 0755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("generation contains special file")
		}
		return os.Chmod(path, 0644)
	})
}

func verifyClientInReleaseSigner(ctx context.Context, inRelease string, trusted []store.RepositoryAPTSigner, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	home, err := os.MkdirTemp("", "tparun-inrelease-verify-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(home)
	if err = os.Chmod(home, 0700); err != nil {
		return "", err
	}
	for i, signer := range trusted {
		keyPath := filepath.Join(home, fmt.Sprintf("trusted-%d.gpg", i))
		if err = os.WriteFile(keyPath, signer.PublicKey, 0600); err != nil {
			return "", err
		}
		cmd := exec.CommandContext(ctx, "gpg", "--batch", "--no-options", "--homedir", home, "--import", keyPath)
		if output, importErr := cmd.CombinedOutput(); importErr != nil {
			return "", fmt.Errorf("load repository signer: %w: %s", importErr, strings.TrimSpace(string(output)))
		}
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	verify := exec.CommandContext(ctx, "gpg", "--batch", "--status-fd=1", "--verify", inRelease)
	verify.Env = append(os.Environ(), "GNUPGHOME="+home)
	status, err := verify.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("InRelease signature verification failed: %w", err)
	}
	fingerprint, err := verifiedOpenPGPSigner(status)
	if err != nil {
		return "", err
	}
	for _, signer := range trusted {
		if strings.EqualFold(signer.Fingerprint, fingerprint) {
			return signer.Fingerprint, nil
		}
	}
	return "", fmt.Errorf("InRelease signer is not in the repository's trusted signer set")
}

func verifyClientSignedGeneration(ctx context.Context, root string, trusted []store.RepositoryAPTSigner, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	home, err := os.MkdirTemp("", "tparun-generation-verify-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(home)
	if err = os.Chmod(home, 0700); err != nil {
		return "", err
	}
	for i, signer := range trusted {
		keyPath := filepath.Join(home, fmt.Sprintf("trusted-%d.gpg", i))
		if err = os.WriteFile(keyPath, signer.PublicKey, 0600); err != nil {
			return "", err
		}
		cmd := exec.CommandContext(ctx, "gpg", "--batch", "--no-options", "--homedir", home, "--import", keyPath)
		if output, importErr := cmd.CombinedOutput(); importErr != nil {
			return "", fmt.Errorf("load repository signer: %w: %s", importErr, strings.TrimSpace(string(output)))
		}
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	inRelease := filepath.Join(root, "dists", "stable", "InRelease")
	verify := exec.CommandContext(ctx, "gpg", "--batch", "--status-fd=1", "--verify", inRelease)
	verify.Env = append(os.Environ(), "GNUPGHOME="+home)
	status, err := verify.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("InRelease signature verification failed: %w", err)
	}
	fingerprint, err := verifiedOpenPGPSigner(status)
	if err != nil {
		return "", err
	}
	trustedFingerprint := ""
	for _, signer := range trusted {
		if strings.EqualFold(signer.Fingerprint, fingerprint) {
			trustedFingerprint = signer.Fingerprint
			break
		}
	}
	if trustedFingerprint == "" {
		return "", fmt.Errorf("InRelease signer is not in the repository's trusted signer set")
	}
	cfg := config.Config{GPGHome: home, GPGFingerprint: trustedFingerprint, PublishTimeout: timeout}
	if err = verifyPublicationCandidatePhaseContext(ctx, root, "", cfg, true); err != nil {
		return "", err
	}
	return trustedFingerprint, nil
}
