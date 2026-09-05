package tpahubproduct

import (
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

const (
	ProductID commerce.ProductID = "tpa-hub"

	CapabilityRepositoryCreate      commerce.CapabilityID = "repository.create"
	CapabilityRepositoryPublish     commerce.CapabilityID = "repository.publish"
	CapabilityRepositoryDownload    commerce.CapabilityID = "repository.download"
	CapabilityRepositoryReadPrivate commerce.CapabilityID = "repository.read.private"

	NanoStorageBytes          int64 = 5_000_000
	NanoEgressBytes           int64 = 12_000_000_000
	NanoOverageBytesPerSecond int64 = 128_000
)

func ValidCapability(capability commerce.CapabilityID) bool {
	switch capability {
	case CapabilityRepositoryCreate, CapabilityRepositoryPublish, CapabilityRepositoryDownload, CapabilityRepositoryReadPrivate:
		return true
	default:
		return false
	}
}

func NanoOffer() commerce.Offer {
	return commerce.Offer{ID: "nano", ProductID: ProductID, Description: "TPA Hub Nano annual plan", Currency: "EUR", AmountMinor: 500, TaxClass: "saas-personal", Grants: []commerce.GrantTemplate{
		{CapabilityID: CapabilityRepositoryCreate, Period: commerce.PeriodAnnual},
		{CapabilityID: CapabilityRepositoryPublish, Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: NanoStorageBytes}},
		{CapabilityID: CapabilityRepositoryDownload, Period: commerce.PeriodAnnual, Limits: commerce.Limits{EgressBytes: NanoEgressBytes}},
		{CapabilityID: CapabilityRepositoryReadPrivate, Period: commerce.PeriodAnnual},
	}}
}

func DefaultGrants(created time.Time) []commerce.Grant {
	offer := NanoOffer()
	grants := make([]commerce.Grant, len(offer.Grants))
	for i, template := range offer.Grants {
		grants[i] = commerce.Grant{ProductID: offer.ProductID, CapabilityID: template.CapabilityID, OfferID: offer.ID, Enabled: true, ValidFrom: created.UTC(), Period: template.Period, Limits: template.Limits, Source: "local", CreatedAt: created.UTC(), UpdatedAt: created.UTC()}
	}
	return grants
}
