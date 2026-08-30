package main

import "fmt"

const NanoStorageBytes int64 = 5_000_000

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
	return fmt.Sprintf("storage-capacity-exceeded: Storage capacity exceeded. Current usage: %s; resulting usage: %s; Nano capacity: %s", formatBytes(e.CurrentBytes), formatBytes(e.ResultingBytes), formatBytes(e.CapacityBytes))
}
