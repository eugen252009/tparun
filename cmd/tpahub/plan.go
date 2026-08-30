package main

import "fmt"

const (
	NanoStorageBytes          int64 = 5_000_000
	NanoEgressBytes           int64 = 1_000_000_000
	NanoOverageBytesPerSecond int64 = 128_000
)

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
	StorageBlockBytes    int64
	EgressBlockBytes     int64
}

var planCatalog = map[string]PlanDefinition{
	"nano":     {Code: "nano", BillingMode: BillingFixed, Available: true, StorageCapacityBytes: NanoStorageBytes, MonthlyEgressBytes: NanoEgressBytes},
	"standard": {Code: "standard", BillingMode: BillingFixed, StorageCapacityBytes: 500_000_000, MonthlyEgressBytes: 20_000_000_000},
	"pro":      {Code: "pro", BillingMode: BillingUsageBased, StorageBlockBytes: 500_000_000, EgressBlockBytes: 20_000_000_000},
}

type StorageCapacityError struct {
	CurrentBytes   int64
	ResultingBytes int64
	CapacityBytes  int64
}

func (e StorageCapacityError) Error() string {
	return fmt.Sprintf("storage-capacity-exceeded: Storage capacity exceeded. Current usage: %s (current_bytes=%d); resulting usage: %s (resulting_bytes=%d); Nano capacity: %s (capacity_bytes=%d)", formatBytes(e.CurrentBytes), e.CurrentBytes, formatBytes(e.ResultingBytes), e.ResultingBytes, formatBytes(e.CapacityBytes), e.CapacityBytes)
}
