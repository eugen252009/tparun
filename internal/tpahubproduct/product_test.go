package tpahubproduct

import (
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func TestNanoOfferCreatesProductCapabilityGrants(t *testing.T) {
	offer := NanoOffer()
	grants := DefaultGrants(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if offer.ProductID != ProductID || offer.Cadence != NanoCadence || len(grants) != 4 {
		t.Fatalf("offer=%+v grants=%+v", offer, grants)
	}
	limits := map[commerce.CapabilityID]commerce.Limits{}
	for _, grant := range grants {
		if grant.ProductID != ProductID || grant.OfferID != offer.ID || grant.Period != NanoCadence {
			t.Fatalf("grant=%+v", grant)
		}
		limits[grant.CapabilityID] = grant.Limits
	}
	if limits[CapabilityRepositoryPublish].StorageBytes != NanoStorageBytes || limits[CapabilityRepositoryDownload].EgressBytes != NanoEgressBytes {
		t.Fatalf("limits=%+v", limits)
	}
}
