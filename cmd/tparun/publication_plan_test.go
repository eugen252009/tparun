package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func TestPublicationPlanUsesCanonicalUsageAndClassifiesExisting(t *testing.T) {
	s, _ := testServer(t)
	id := "abababababababababababababababab"
	root := storageTestRepository(t, s, id)
	packagePath := filepath.Join(t.TempDir(), "test_1_amd64.deb")
	buildSemanticTestDebForArchitecture(t, packagePath, "amd64")
	data, err := os.ReadFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.Base(packagePath)), data, 0600); err != nil {
		t.Fatal(err)
	}
	identityPackage, version, architecture, err := parseDebIdentity(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	plan, err := s.publicationPlan(context.Background(), "a", id, []publicationPlanInput{{
		Filename: filepath.Base(packagePath), Package: identityPackage, Version: version, Architecture: architecture,
		Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Allowed || plan.AlreadyPresentCount != 1 || plan.NewPackageCount != 0 || plan.ProjectedPackageCount != 1 || plan.ProjectedStorageBytes != int64(len(data)) {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestPublicationPlanRejectsOverQuotaBeforeUpload(t *testing.T) {
	s, _ := testServer(t)
	id := "edededededededededededededededed"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: id, AccountID: "a", Name: id, Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	s.grants = grantServiceFunc(func(context.Context, commerce.CustomerID, commerce.ProductID, commerce.CapabilityID, time.Time) (commerce.Grant, error) {
		return commerce.Grant{CustomerID: "a", ProductID: tpahubproduct.ProductID, CapabilityID: CapabilityRepositoryPublish, Enabled: true, Limits: commerce.Limits{StorageBytes: 100}}, nil
	})
	plan, err := s.publicationPlan(context.Background(), "a", id, []publicationPlanInput{{Filename: "large.deb", Package: "large", Version: "1", Architecture: "amd64", Size: 101, SHA256: strings.Repeat("a", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Allowed || plan.RemainingStorageBytes != -1 || plan.Reason == "" {
		t.Fatalf("unexpected over-quota plan: %+v", plan)
	}
}
