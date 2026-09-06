package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

const (
	TPAHubProductID                 = tpahubproduct.ProductID
	CapabilityRepositoryCreate      = tpahubproduct.CapabilityRepositoryCreate
	CapabilityRepositoryPublish     = tpahubproduct.CapabilityRepositoryPublish
	CapabilityRepositoryDownload    = tpahubproduct.CapabilityRepositoryDownload
	CapabilityRepositoryReadPrivate = tpahubproduct.CapabilityRepositoryReadPrivate

	NanoStorageBytes          int64 = tpahubproduct.NanoStorageBytes
	NanoEgressBytes           int64 = tpahubproduct.NanoEgressBytes
	NanoOverageBytesPerSecond int64 = tpahubproduct.NanoOverageBytesPerSecond
)

type ProductTaxClassification string

const (
	TaxSaaSPersonal ProductTaxClassification = "saas-personal"
	TaxSaaSBusiness ProductTaxClassification = "saas-business"
)

type PlanCadence string

const (
	CadenceContinuous PlanCadence = "continuous"
	CadenceMonthly    PlanCadence = "monthly"
	CadenceAnnual     PlanCadence = "annual"
)

type EntitlementPeriod struct {
	Start time.Time
	End   time.Time
}

type BillingMode string

const (
	BillingFixed      BillingMode = "fixed"
	BillingUsageBased BillingMode = "usage-based"
)

type PlanDefinition struct {
	Code                 string
	BillingMode          BillingMode
	Available            bool
	StorageCapacityBytes int64
	MonthlyEgressBytes   int64
	EgressAllowanceBytes int64
	EgressCadence        PlanCadence
	BillingCadence       PlanCadence
	TaxClassification    ProductTaxClassification
	StorageBlockBytes    int64
	EgressBlockBytes     int64
}

func tpaNanoOffer() commerce.Offer { return tpahubproduct.NanoOffer() }
func defaultTPAGrants(createdAt string) []commerce.Grant {
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		created = time.Now().UTC()
	}
	return tpahubproduct.DefaultGrants(created)
}

func grantDenied(err error) bool {
	return errors.Is(err, commerce.ErrGrantNotFound) || errors.Is(err, commerce.ErrGrantDisabled) || errors.Is(err, commerce.ErrGrantInactive)
}

type nanoFloorGrantService struct{ server *server }

func (g nanoFloorGrantService) GetGrant(ctx context.Context, customer commerce.CustomerID, product commerce.ProductID, capability commerce.CapabilityID, at time.Time) (commerce.Grant, error) {
	grant, err := g.server.db.GetGrant(ctx, customer, product, capability, at)
	if product != tpahubproduct.ProductID || !tpahubproduct.ValidCapability(capability) {
		return grant, err
	}
	created, createdErr := g.server.db.AccountCreatedAt(ctx, string(customer))
	if createdErr != nil {
		return commerce.Grant{}, createdErr
	}
	var floor commerce.Grant
	for _, candidate := range tpahubproduct.DefaultGrants(created) {
		if candidate.CapabilityID == capability {
			floor = candidate
			floor.CustomerID = customer
			break
		}
	}
	if floor.CapabilityID == "" {
		return grant, err
	}
	return resolveNanoGrant(grant, err, floor)
}

func resolveNanoGrant(grant commerce.Grant, err error, floor commerce.Grant) (commerce.Grant, error) {
	if err != nil {
		if errors.Is(err, commerce.ErrGrantNotFound) || errors.Is(err, commerce.ErrGrantDisabled) || errors.Is(err, commerce.ErrGrantInactive) {
			return floor, nil
		}
		return commerce.Grant{}, err
	}
	// Negative persisted limits are corruption, not a below-floor entitlement;
	// leave them visible so resource-specific callers fail closed.
	if grant.Limits.StorageBytes < 0 || grant.Limits.EgressBytes < 0 {
		return grant, nil
	}
	if grant.Limits.StorageBytes >= floor.Limits.StorageBytes && grant.Limits.EgressBytes >= floor.Limits.EgressBytes {
		return grant, nil
	}
	grant.Limits.StorageBytes = max(grant.Limits.StorageBytes, floor.Limits.StorageBytes)
	grant.Limits.EgressBytes = max(grant.Limits.EgressBytes, floor.Limits.EgressBytes)
	grant.Source, grant.OfferID, grant.Period = "effective", floor.OfferID, floor.Period
	grant.ValidFrom, grant.ValidUntil, grant.Effective = floor.ValidFrom, nil, nil
	return grant, nil
}

func (s *server) grantService() commerce.GrantService {
	if s.grants != nil {
		return s.grants
	}
	return nanoFloorGrantService{server: s}
}

func effectiveEntitlementPeriod(entitlement store.AccountEntitlement, now time.Time) (EntitlementPeriod, error) {
	active, err := entitlement.ActiveAt(now)
	if err != nil {
		return EntitlementPeriod{}, err
	}
	if !active {
		return EntitlementPeriod{}, fmt.Errorf("entitlement inactive")
	}
	if entitlement.Source != "purchase" {
		return entitlementPeriod(entitlement.ValidFrom, now)
	}
	start, err := time.Parse(time.RFC3339Nano, entitlement.ValidFrom)
	if err != nil {
		return EntitlementPeriod{}, err
	}
	end, err := time.Parse(time.RFC3339Nano, entitlement.ValidUntil)
	if err != nil {
		return EntitlementPeriod{}, err
	}
	return EntitlementPeriod{Start: start.UTC(), End: end.UTC()}, nil
}

func entitlementPeriod(startedAt string, now time.Time) (EntitlementPeriod, error) {
	return entitlementPeriodForCadence(startedAt, now, tpahubproduct.NanoCadence)
}

func entitlementPeriodForCadence(startedAt string, now time.Time, cadence commerce.PeriodCadence) (EntitlementPeriod, error) {
	anchor, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		anchor, err = time.Parse("2006-01-02", startedAt)
	}
	if err != nil {
		return EntitlementPeriod{}, fmt.Errorf("invalid entitlement anchor: %w", err)
	}
	anchor = time.Date(anchor.UTC().Year(), anchor.UTC().Month(), anchor.UTC().Day(), 0, 0, 0, 0, time.UTC)
	now = now.UTC()
	var start time.Time
	switch cadence {
	case commerce.PeriodAnnual:
		years := now.Year() - anchor.Year()
		start = anchor.AddDate(years, 0, 0)
		if now.Before(start) {
			start = anchor.AddDate(years-1, 0, 0)
		}
	case commerce.PeriodMonthly:
		months := (now.Year()-anchor.Year())*12 + int(now.Month()-anchor.Month())
		start = anchor.AddDate(0, months, 0)
		if now.Before(start) {
			start = anchor.AddDate(0, months-1, 0)
		}
	default:
		return EntitlementPeriod{}, fmt.Errorf("unsupported entitlement cadence: %s", cadence)
	}
	end, err := advanceCadence(start, cadence)
	if err != nil {
		return EntitlementPeriod{}, err
	}
	return EntitlementPeriod{Start: start, End: end}, nil
}

func advanceCadence(start time.Time, cadence commerce.PeriodCadence) (time.Time, error) {
	switch cadence {
	case commerce.PeriodAnnual:
		return start.AddDate(1, 0, 0), nil
	case commerce.PeriodMonthly:
		return start.AddDate(0, 1, 0), nil
	default:
		return time.Time{}, fmt.Errorf("unsupported calendar cadence: %s", cadence)
	}
}

func nanoOfferLimits(capability commerce.CapabilityID) commerce.Limits {
	for _, grant := range tpahubproduct.NanoOffer().Grants {
		if grant.CapabilityID == capability {
			return grant.Limits
		}
	}
	panic("Nano offer is missing capability " + string(capability))
}

var planCatalog = map[string]PlanDefinition{
	"nano":     {Code: "nano", BillingMode: BillingFixed, Available: true, StorageCapacityBytes: nanoOfferLimits(CapabilityRepositoryPublish).StorageBytes, MonthlyEgressBytes: nanoOfferLimits(CapabilityRepositoryDownload).EgressBytes, EgressAllowanceBytes: nanoOfferLimits(CapabilityRepositoryDownload).EgressBytes, EgressCadence: PlanCadence(tpahubproduct.NanoCadence), BillingCadence: PlanCadence(tpahubproduct.NanoCadence), TaxClassification: TaxSaaSPersonal},
	"standard": {Code: "standard", BillingMode: BillingFixed, StorageCapacityBytes: 500_000_000, EgressAllowanceBytes: 20_000_000_000, EgressCadence: CadenceMonthly, BillingCadence: CadenceMonthly, TaxClassification: TaxSaaSPersonal},
	"pro":      {Code: "pro", BillingMode: BillingUsageBased, EgressCadence: CadenceMonthly, BillingCadence: CadenceMonthly, TaxClassification: TaxSaaSBusiness, StorageBlockBytes: 500_000_000, EgressBlockBytes: 20_000_000_000},
}

type StorageCapacityError struct {
	CurrentBytes   int64
	ResultingBytes int64
	CapacityBytes  int64
}

func (e StorageCapacityError) Error() string {
	return fmt.Sprintf("storage-capacity-exceeded: Storage capacity exceeded. Current usage: %s (current_bytes=%d); resulting usage: %s (resulting_bytes=%d); capacity: %s (capacity_bytes=%d)", formatBytes(e.CurrentBytes), e.CurrentBytes, formatBytes(e.ResultingBytes), e.ResultingBytes, formatBytes(e.CapacityBytes), e.CapacityBytes)
}
