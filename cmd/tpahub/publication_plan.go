package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type publicationPlanInput struct {
	Filename     string `json:"filename"`
	Package      string `json:"package"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
}

type publicationPlanItem struct {
	Filename       string `json:"filename"`
	Package        string `json:"package"`
	Version        string `json:"version"`
	Architecture   string `json:"architecture"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	Classification string `json:"classification"`
}

type publicationPlanResponse struct {
	RepositoryID                 string                `json:"repository_id"`
	CurrentPackageCount          int64                 `json:"current_package_count"`
	CurrentStorageBytes          int64                 `json:"current_storage_bytes"`
	StagedPackageCount           int64                 `json:"staged_package_count"`
	StagedStorageBytes           int64                 `json:"staged_storage_bytes"`
	NewPackageCount              int64                 `json:"new_package_count"`
	ReplacementCount             int64                 `json:"replacement_count"`
	AlreadyPresentCount          int64                 `json:"already_present_count"`
	ProjectedPackageCount        int64                 `json:"projected_package_count"`
	ProjectedStorageBytes        int64                 `json:"projected_storage_bytes"`
	AccountStorageBytes          int64                 `json:"account_storage_bytes"`
	ProjectedAccountStorageBytes int64                 `json:"projected_account_storage_bytes"`
	StorageLimitBytes            int64                 `json:"storage_limit_bytes"`
	RemainingStorageBytes        int64                 `json:"remaining_storage_bytes"`
	Allowed                      bool                  `json:"allowed"`
	Reason                       string                `json:"reason,omitempty"`
	Items                        []publicationPlanItem `json:"items"`
}

type existingPublicationArtifact struct {
	filename string
	size     int64
	sha256   string
}

func (s *server) publicationPlan(ctx context.Context, accountID, repositoryID string, inputs []publicationPlanInput) (publicationPlanResponse, error) {
	repo, grant, err := s.repositoryPolicy().AuthorizePublication(ctx, accountID, repositoryID)
	if err != nil {
		return publicationPlanResponse{}, err
	}
	if len(inputs) == 0 {
		return publicationPlanResponse{}, fmt.Errorf("at least one staged package is required")
	}
	root, exists, err := s.repositoryUsageRoot(repositoryID)
	if err != nil {
		return publicationPlanResponse{}, fmt.Errorf("repository storage unavailable: %w", err)
	}
	existing := map[string]existingPublicationArtifact{}
	var current PublishedUsage
	if exists {
		current, err = publishedUsage(root)
		if err != nil {
			return publicationPlanResponse{}, fmt.Errorf("repository storage unavailable: %w", err)
		}
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !entry.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".deb") {
				return nil
			}
			identity, identityErr := debIdentity(ctx, path)
			if identityErr != nil {
				return fmt.Errorf("read published package %s: %w", entry.Name(), identityErr)
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			digest, digestErr := fileSHA256(path)
			if digestErr != nil {
				return digestErr
			}
			if _, duplicate := existing[identity]; duplicate {
				return fmt.Errorf("duplicate published package identity: %s", identity)
			}
			existing[identity] = existingPublicationArtifact{filename: entry.Name(), size: info.Size(), sha256: digest}
			return nil
		})
		if err != nil {
			return publicationPlanResponse{}, err
		}
	}
	seen := map[string]bool{}
	items := make([]publicationPlanItem, 0, len(inputs))
	projectedBytes := current.StoredBytes
	var newCount, replacements, already int64
	for _, input := range inputs {
		if filepath.Base(input.Filename) != input.Filename || !strings.HasSuffix(strings.ToLower(input.Filename), ".deb") || input.Package == "" || input.Version == "" || input.Architecture == "" || input.Size <= 0 || len(input.SHA256) != sha256.Size*2 {
			return publicationPlanResponse{}, fmt.Errorf("invalid staged package metadata for %q", input.Filename)
		}
		if _, err := hex.DecodeString(input.SHA256); err != nil {
			return publicationPlanResponse{}, fmt.Errorf("invalid staged SHA256 for %q", input.Filename)
		}
		identity := strings.Join([]string{input.Package, input.Version, input.Architecture}, "\x00")
		if seen[identity] {
			return publicationPlanResponse{}, fmt.Errorf("duplicate staged package identity: %s", identity)
		}
		seen[identity] = true
		classification := "NEW"
		if prior, ok := existing[identity]; ok {
			if prior.size == input.Size && strings.EqualFold(prior.sha256, input.SHA256) {
				classification = "ALREADY_PRESENT"
				already++
			} else {
				classification = "REPLACEMENT"
				replacements++
				projectedBytes -= prior.size
				projectedBytes += input.Size
			}
		} else {
			newCount++
			projectedBytes += input.Size
		}
		items = append(items, publicationPlanItem{Filename: input.Filename, Package: input.Package, Version: input.Version, Architecture: input.Architecture, Size: input.Size, SHA256: strings.ToLower(input.SHA256), Classification: classification})
	}
	if projectedBytes < 0 {
		return publicationPlanResponse{}, fmt.Errorf("projected repository storage overflow")
	}
	accountUsage, err := s.accountUsage(ctx, repo.AccountID)
	if err != nil {
		return publicationPlanResponse{}, fmt.Errorf("storage usage unavailable: %w", err)
	}
	projectedAccount, overflow := addCapacity(accountUsage.StoredBytes-current.StoredBytes, projectedBytes)
	if overflow {
		return publicationPlanResponse{}, fmt.Errorf("projected account storage overflow")
	}
	allowed := projectedAccount <= grant.Limits.StorageBytes || projectedAccount <= accountUsage.StoredBytes
	response := publicationPlanResponse{
		RepositoryID: repositoryID, CurrentPackageCount: current.PackageCount, CurrentStorageBytes: current.StoredBytes,
		StagedPackageCount: int64(len(inputs)), NewPackageCount: newCount, ReplacementCount: replacements, AlreadyPresentCount: already,
		ProjectedPackageCount: current.PackageCount + newCount, ProjectedStorageBytes: projectedBytes,
		AccountStorageBytes: accountUsage.StoredBytes, ProjectedAccountStorageBytes: projectedAccount,
		StorageLimitBytes: grant.Limits.StorageBytes, RemainingStorageBytes: grant.Limits.StorageBytes - projectedAccount,
		Allowed: allowed, Items: items,
	}
	for _, item := range inputs {
		response.StagedStorageBytes += item.Size
	}
	if !allowed {
		response.Reason = "publication would exceed storage capacity"
	}
	sort.Slice(response.Items, func(i, j int) bool { return response.Items[i].Filename < response.Items[j].Filename })
	return response, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *server) handlePublicationPlan(w http.ResponseWriter, r *http.Request, repositoryID string) {
	if r.Method != "POST" {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var inputs []publicationPlanInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&inputs); err != nil {
		bad(w, http.StatusBadRequest, "invalid publication plan")
		return
	}
	plan, err := s.publicationPlan(r.Context(), accountID(r), repositoryID, inputs)
	if err != nil {
		bad(w, http.StatusForbidden, err.Error())
		return
	}
	if !plan.Allowed {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = json.NewEncoder(w).Encode(plan)
		return
	}
	jsonOut(w, plan)
}
