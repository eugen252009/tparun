package main

import "fmt"

const (
	NanoStorageBytes int64 = 5_000_000
	NanoEgressBytes  int64 = 1_000_000_000
)

type PlanLimits struct {
	Name         string
	StorageBytes int64
}

var nanoPlan = PlanLimits{Name: "Nano", StorageBytes: NanoStorageBytes}

type StorageCapacityError struct {
	CurrentBytes   int64
	ResultingBytes int64
	CapacityBytes  int64
}

func (e StorageCapacityError) Error() string {
	return fmt.Sprintf("storage-capacity-exceeded: Storage capacity exceeded. Current usage: %s (current_bytes=%d); resulting usage: %s (resulting_bytes=%d); Nano capacity: %s (capacity_bytes=%d)", formatBytes(e.CurrentBytes), e.CurrentBytes, formatBytes(e.ResultingBytes), e.ResultingBytes, formatBytes(e.CapacityBytes), e.CapacityBytes)
}
