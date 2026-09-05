package tpahubproduct

import (
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func TestNanoOfferCreatesProductCapabilityGrants(t *testing.T) {
	offer := NanoOffer()
	grants := DefaultGrants(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if offer.ProductID != ProductID || offer.ID != "nano" || offer.Version != NanoOfferVersion || offer.AmountMinor != NanoPriceMinor || offer.Currency != "EUR" || offer.Cadence != NanoCadence || len(offer.Grants) != 4 || len(grants) != 4 {
		t.Fatalf("offer=%+v grants=%+v", offer, grants)
	}
	wantLimits := map[commerce.CapabilityID]commerce.Limits{
		CapabilityRepositoryCreate:      {},
		CapabilityRepositoryPublish:     {StorageBytes: NanoStorageBytes},
		CapabilityRepositoryDownload:    {EgressBytes: NanoEgressBytes},
		CapabilityRepositoryReadPrivate: {},
	}
	limits := map[commerce.CapabilityID]commerce.Limits{}
	for _, grant := range grants {
		if grant.ProductID != ProductID || grant.OfferID != offer.ID || grant.Period != NanoCadence {
			t.Fatalf("grant=%+v", grant)
		}
		limits[grant.CapabilityID] = grant.Limits
	}
	if len(limits) != len(wantLimits) {
		t.Fatalf("limits=%+v want=%+v", limits, wantLimits)
	}
	for capability, want := range wantLimits {
		if limits[capability] != want {
			t.Fatalf("limit[%s]=%+v want=%+v", capability, limits[capability], want)
		}
	}
	for _, grant := range offer.Grants {
		if limits[grant.CapabilityID] != grant.Limits {
			t.Fatalf("offer limit[%s]=%+v default=%+v", grant.CapabilityID, grant.Limits, limits[grant.CapabilityID])
		}
	}
	if limits[CapabilityRepositoryPublish].StorageBytes != NanoStorageBytes || limits[CapabilityRepositoryDownload].EgressBytes != NanoEgressBytes {
		t.Fatalf("limits=%+v", limits)
	}
}
