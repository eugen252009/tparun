package main

import (
	"errors"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func TestNanoFloorChangesWithoutMutatingRawGrant(t *testing.T) {
	raw := commerce.Grant{CapabilityID: CapabilityRepositoryPublish, Limits: commerce.Limits{StorageBytes: 3, EgressBytes: 3}, Source: "test"}
	floorA := commerce.Grant{CapabilityID: CapabilityRepositoryPublish, OfferID: "nano-a", Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 5, EgressBytes: 5}, ValidFrom: time.Unix(1, 0)}
	floorB := floorA
	floorB.OfferID, floorB.Limits.StorageBytes, floorB.Limits.EgressBytes = "nano-b", 10, 10
	gotA, err := resolveNanoGrant(raw, nil, floorA)
	if err != nil || gotA.Limits.StorageBytes != 5 || gotA.Limits.EgressBytes != 5 {
		t.Fatalf("floor A=%+v err=%v", gotA, err)
	}
	gotB, err := resolveNanoGrant(raw, nil, floorB)
	if err != nil || gotB.Limits.StorageBytes != 10 || gotB.Limits.EgressBytes != 10 {
		t.Fatalf("floor B=%+v err=%v", gotB, err)
	}
	if raw.Limits.StorageBytes != 3 || raw.Limits.EgressBytes != 3 {
		t.Fatalf("raw grant mutated=%+v", raw)
	}
}

func TestNanoFloorPreservesHigherAndRejectsInvalid(t *testing.T) {
	floor := commerce.Grant{CapabilityID: CapabilityRepositoryDownload, Limits: commerce.Limits{StorageBytes: 5, EgressBytes: 10}}
	higher := commerce.Grant{CapabilityID: floor.CapabilityID, Limits: commerce.Limits{StorageBytes: 100, EgressBytes: 100}}
	got, err := resolveNanoGrant(higher, nil, floor)
	if err != nil || got.Limits != higher.Limits {
		t.Fatalf("higher grant changed=%+v err=%v", got, err)
	}
	invalid := higher
	invalid.Limits.EgressBytes = -1
	got, err = resolveNanoGrant(invalid, nil, floor)
	if err != nil || got.Limits.EgressBytes != -1 {
		t.Fatalf("negative grant was not preserved for fail-closed validation: %+v err=%v", got, err)
	}
	if _, err = resolveNanoGrant(commerce.Grant{}, errors.New("Nano policy unavailable"), floor); err == nil {
		t.Fatal("policy failure was softened")
	}
}
