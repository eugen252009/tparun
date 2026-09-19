package main

import (
	"math"
	"strings"
	"testing"
)

func TestPublicationPlanOutputFiniteSummaryUsesAuthoritativeDeltas(t *testing.T) {
	out := publicationPlanOutput(publicationPlanResponse{
		CurrentPackageCount:          13,
		CurrentStorageBytes:          39_600_000,
		AccountStorageBytes:          39_600_000,
		ProjectedPackageCount:        15,
		ProjectedStorageBytes:        45_600_000,
		ProjectedAccountStorageBytes: 45_600_000,
		StorageLimitBytes:            100_000_000,
		NewPackageCount:              2,
	}, false)
	for _, want := range []string{"New repository statistics", "15 packages (+2)", "45.6 MB / 100.0 MB (+6.0 MB)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q in %q", want, out)
		}
	}
	for _, unwanted := range []string{"Current", "Staged", "Repository:"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("default summary contains verbose label %q: %q", unwanted, out)
		}
	}
}

func TestPublicationPlanOutputUnlimitedOmitsCapacityLanguage(t *testing.T) {
	out := publicationPlanOutput(publicationPlanResponse{
		ProjectedPackageCount:        15,
		CurrentStorageBytes:          39_600_000,
		ProjectedStorageBytes:        45_600_000,
		AccountStorageBytes:          39_600_000,
		ProjectedAccountStorageBytes: 45_600_000,
		StorageLimitBytes:            math.MaxInt64,
		NewPackageCount:              2,
	}, false)
	if !strings.Contains(out, "45.6 MB (+6.0 MB)") {
		t.Fatalf("unlimited summary missing: %q", out)
	}
	for _, unwanted := range []string{"/", "unlimited", "Unlimited", "Remaining", "Capacity"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("unlimited summary contains %q: %q", unwanted, out)
		}
	}
}

func TestPublicationPlanOutputVerboseIncludesPlanAndSummary(t *testing.T) {
	out := publicationPlanOutput(publicationPlanResponse{
		CurrentPackageCount:          13,
		CurrentStorageBytes:          39_600_000,
		ProjectedPackageCount:        15,
		ProjectedStorageBytes:        45_600_000,
		AccountStorageBytes:          39_600_000,
		ProjectedAccountStorageBytes: 45_600_000,
		StorageLimitBytes:            100_000_000,
		NewPackageCount:              2,
		Items: []publicationPlanItem{
			{Classification: "NEW", Package: "my-app", Version: "1.2.0", Architecture: "amd64", Size: 4_800_000},
			{Classification: "REPLACEMENT", Package: "my-cli", Version: "1.2.0", Architecture: "amd64", Size: 1_200_000},
			{Classification: "ALREADY_PRESENT", Package: "common", Version: "1.0.0", Architecture: "all", Size: 0},
		},
	}, true)
	for _, want := range []string{"Staged packages", "NEW", "REPLACEMENT", "ALREADY_PRESENT", "Current repository", "New repository statistics", "15 packages (+2)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("verbose output missing %q in %q", want, out)
		}
	}
}
