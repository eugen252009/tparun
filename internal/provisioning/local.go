// Package provisioning produces initial grant facts, not runtime decisions.
// Callers persist these with the account in the existing store transaction.
package provisioning

import (
	"fmt"
	"math"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/tpahubcapability"
)

// Source is a statically composed producer for account creation only. It cannot
// persist grants, authorize requests, or contact billing/payment services.
type Source interface {
	InitialGrants(created time.Time) []commerce.Grant
}

// LocalPolicy is trusted operator configuration. Finite byte values, including
// zero, retain their existing grant meanings. PrivateRead disables only the
// existing private-read capability; it is not a new visibility policy.
type LocalPolicy struct {
	StorageBytes int64
	EgressBytes  int64
	PrivateRead  bool
}

// DefaultLocalPolicy deliberately preserves the former local numeric bounds
// and annual cadence, but owns them independently of any commercial catalog.
func DefaultLocalPolicy() LocalPolicy {
	return LocalPolicy{StorageBytes: 5_000_000, EgressBytes: 12_000_000_000, PrivateRead: true}
}

func (p LocalPolicy) Validate() error {
	if p.StorageBytes < 0 || p.EgressBytes < 0 || p.StorageBytes == math.MaxInt64 || p.EgressBytes == math.MaxInt64 {
		return fmt.Errorf("local policy byte limits must be finite integers between 0 and %d", int64(math.MaxInt64-1))
	}
	return nil
}

func (p LocalPolicy) InitialGrants(created time.Time) []commerce.Grant {
	created = created.UTC()
	specs := []struct {
		capability commerce.CapabilityID
		enabled    bool
		limits     commerce.Limits
	}{
		{tpahubcapability.RepositoryCreate, true, commerce.Limits{}},
		{tpahubcapability.RepositoryPublish, true, commerce.Limits{StorageBytes: p.StorageBytes}},
		{tpahubcapability.RepositoryDownload, true, commerce.Limits{EgressBytes: p.EgressBytes}},
		{tpahubcapability.RepositoryReadPrivate, p.PrivateRead, commerce.Limits{}},
	}
	grants := make([]commerce.Grant, len(specs))
	for i, spec := range specs {
		grants[i] = commerce.Grant{ProductID: tpahubcapability.ProductID, CapabilityID: spec.capability, OfferID: "local-policy-v1", Enabled: spec.enabled, ValidFrom: created, Period: commerce.PeriodAnnual, Limits: spec.limits, Source: "local", CreatedAt: created, UpdatedAt: created}
	}
	return grants
}

var _ Source = LocalPolicy{}
