package tpahubproduct

import (
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func TestNanoOfferCreatesProductCapabilityGrants(t *testing.T) {
	offer := NanoOffer()
	if offer.ProductID != commerce.ProductID("tpa-hub") || offer.ID != "nano" || offer.Version != "nano-v1" || offer.AmountMinor != 500 || offer.Currency != "EUR" || offer.Cadence != commerce.PeriodAnnual || offer.TaxClass != "saas-personal" {
		t.Fatalf("offer=%+v", offer)
	}
	wantLimits := map[commerce.CapabilityID]commerce.Limits{
		CapabilityRepositoryCreate:      {},
		CapabilityRepositoryPublish:     {StorageBytes: 5_000_000},
		CapabilityRepositoryDownload:    {EgressBytes: 12_000_000_000},
		CapabilityRepositoryReadPrivate: {},
	}
	if len(offer.Grants) != len(wantLimits) {
		t.Fatalf("grant count=%d want=%d", len(offer.Grants), len(wantLimits))
	}
	seen := make(map[commerce.CapabilityID]bool, len(offer.Grants))
	for _, grant := range offer.Grants {
		if seen[grant.CapabilityID] {
			t.Fatalf("duplicate capability=%s", grant.CapabilityID)
		}
		seen[grant.CapabilityID] = true
		want, ok := wantLimits[grant.CapabilityID]
		if !ok || grant.Period != commerce.PeriodAnnual || grant.Limits != want {
			t.Fatalf("grant=%+v", grant)
		}
	}
	if len(seen) != len(wantLimits) {
		t.Fatalf("capabilities=%v want=%v", seen, wantLimits)
	}
	grants := DefaultGrants(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(grants) != len(wantLimits) {
		t.Fatalf("default grant count=%d", len(grants))
	}
	for _, grant := range grants {
		if grant.ProductID != offer.ProductID || grant.OfferID != offer.ID || grant.Period != offer.Cadence || grant.Limits != wantLimits[grant.CapabilityID] {
			t.Fatalf("default grant=%+v", grant)
		}
	}
}

func TestMinEphemeralProfileContract(t *testing.T) {
	created := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	grants := MinEphemeralGrants(created)
	if len(grants) != 4 {
		t.Fatalf("grant count=%d", len(grants))
	}
	want := map[string]struct{ storage, egress int64 }{
		string(CapabilityRepositoryCreate):      {0, 0},
		string(CapabilityRepositoryPublish):     {10240, 0},
		string(CapabilityRepositoryDownload):    {0, 20480},
		string(CapabilityRepositoryReadPrivate): {0, 0},
	}
	for _, grant := range grants {
		limits, ok := want[string(grant.CapabilityID)]
		if !ok {
			t.Fatalf("unexpected capability %q", grant.CapabilityID)
		}
		if grant.OfferID != "ephemeral-min" || grant.Limits.StorageBytes != limits.storage || grant.Limits.EgressBytes != limits.egress || !grant.Enabled || grant.Source != "local" {
			t.Fatalf("unexpected grant %+v", grant)
		}
		delete(want, string(grant.CapabilityID))
	}
	if len(want) != 0 {
		t.Fatalf("missing capabilities=%v", want)
	}
	if MinEphemeralLifetime != 5*time.Minute || MinEphemeralPurgeDelay != 10*time.Minute || MinEphemeralStorage != 10240 || MinEphemeralEgress != 20480 {
		t.Fatal("fixed ephemeral-min constants changed")
	}
}
