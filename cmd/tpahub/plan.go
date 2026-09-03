package main

import (
	"fmt"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

const (
	NanoStorageBytes          int64 = 5_000_000
	NanoEgressBytes           int64 = 12_000_000_000
	NanoOverageBytesPerSecond int64 = 128_000
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
	anchor, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		anchor, err = time.Parse("2006-01-02", startedAt)
	}
	if err != nil {
		return EntitlementPeriod{}, fmt.Errorf("invalid entitlement anchor: %w", err)
	}
	anchor = time.Date(anchor.UTC().Year(), anchor.UTC().Month(), anchor.UTC().Day(), 0, 0, 0, 0, time.UTC)
	now = now.UTC()
	years := now.Year() - anchor.Year()
	start := anchor.AddDate(years, 0, 0)
	if now.Before(start) {
		years--
		start = anchor.AddDate(years, 0, 0)
	}
	return EntitlementPeriod{Start: start, End: start.AddDate(1, 0, 0)}, nil
}

var planCatalog = map[string]PlanDefinition{
	"nano":     {Code: "nano", BillingMode: BillingFixed, Available: true, StorageCapacityBytes: NanoStorageBytes, MonthlyEgressBytes: NanoEgressBytes, EgressAllowanceBytes: NanoEgressBytes, EgressCadence: CadenceAnnual, BillingCadence: CadenceAnnual, TaxClassification: TaxSaaSPersonal},
	"standard": {Code: "standard", BillingMode: BillingFixed, StorageCapacityBytes: 500_000_000, EgressAllowanceBytes: 20_000_000_000, EgressCadence: CadenceMonthly, BillingCadence: CadenceMonthly, TaxClassification: TaxSaaSPersonal},
	"pro":      {Code: "pro", BillingMode: BillingUsageBased, EgressCadence: CadenceMonthly, BillingCadence: CadenceMonthly, TaxClassification: TaxSaaSBusiness, StorageBlockBytes: 500_000_000, EgressBlockBytes: 20_000_000_000},
}

type StorageCapacityError struct {
	CurrentBytes   int64
	ResultingBytes int64
	CapacityBytes  int64
}

func (e StorageCapacityError) Error() string {
	return fmt.Sprintf("storage-capacity-exceeded: Storage capacity exceeded. Current usage: %s (current_bytes=%d); resulting usage: %s (resulting_bytes=%d); Nano capacity: %s (capacity_bytes=%d)", formatBytes(e.CurrentBytes), e.CurrentBytes, formatBytes(e.ResultingBytes), e.ResultingBytes, formatBytes(e.CapacityBytes), e.CapacityBytes)
}
