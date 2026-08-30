package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

type BillingPeriod string

type BillingUsageSnapshot struct {
	AccountID             string        `json:"account_id"`
	PlanCode              string        `json:"plan_code"`
	Period                BillingPeriod `json:"period"`
	CurrentStorageBytes   int64         `json:"current_storage_bytes"`
	StorageHighWaterBytes int64         `json:"storage_high_water_bytes"`
	MonthlyEgressBytes    int64         `json:"monthly_egress_bytes"`
}

type BillingPreview struct {
	AccountID             string        `json:"account_id"`
	PlanCode              string        `json:"plan_code"`
	Period                BillingPeriod `json:"period"`
	StorageHighWaterBytes int64         `json:"storage_high_water_bytes"`
	EgressBytes           int64         `json:"egress_bytes"`
	StorageBlocks         int64         `json:"storage_blocks"`
	EgressBlocks          int64         `json:"egress_blocks"`
}

type BillingProvider interface {
	Preview(context.Context, BillingUsageSnapshot) (BillingPreview, error)
	Finalize(context.Context, BillingUsageSnapshot) (BillingPreview, error)
}

type MockBillingProvider struct {
	Store *store.Store
	Now   func() time.Time
}

func (m MockBillingProvider) Preview(ctx context.Context, snapshot BillingUsageSnapshot) (BillingPreview, error) {
	result := rateBilling(snapshot)
	return result, m.record(ctx, snapshot, "billing-preview-generated", result)
}
func (m MockBillingProvider) Finalize(ctx context.Context, snapshot BillingUsageSnapshot) (BillingPreview, error) {
	result := rateBilling(snapshot)
	return result, m.record(ctx, snapshot, "period-finalized", result)
}
func (m MockBillingProvider) record(ctx context.Context, s BillingUsageSnapshot, event string, result BillingPreview) error {
	if m.Store == nil {
		return fmt.Errorf("mock billing store unavailable")
	}
	payload, e := json.Marshal(result)
	if e != nil {
		return e
	}
	now := time.Now().UTC()
	if m.Now != nil {
		now = m.Now().UTC()
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s", s.AccountID, s.Period, event)))
	return m.Store.RecordMockBillingEvent(ctx, hex.EncodeToString(h[:]), s.AccountID, string(s.Period), event, string(payload), now.Format(time.RFC3339Nano))
}

func rateBilling(s BillingUsageSnapshot) BillingPreview {
	p := planCatalog[s.PlanCode]
	result := BillingPreview{AccountID: s.AccountID, PlanCode: s.PlanCode, Period: s.Period, StorageHighWaterBytes: s.StorageHighWaterBytes, EgressBytes: s.MonthlyEgressBytes}
	if p.BillingMode == BillingUsageBased {
		result.StorageBlocks = ceilBlocks(s.StorageHighWaterBytes, p.StorageBlockBytes)
		result.EgressBlocks = ceilBlocks(s.MonthlyEgressBytes, p.EgressBlockBytes)
	}
	return result
}
func ceilBlocks(value, block int64) int64 {
	if value <= 0 || block <= 0 {
		return 0
	}
	return (value + block - 1) / block
}

func (s *server) billingSnapshot(ctx context.Context, accountID string, now time.Time) (BillingUsageSnapshot, error) {
	period := BillingPeriod(now.UTC().Format("2006-01"))
	usage, e := s.accountUsage(ctx, accountID)
	if e != nil {
		return BillingUsageSnapshot{}, e
	}
	if e = s.db.ObserveStorageHighWater(ctx, accountID, string(period), usage.StoredBytes, now.UTC().Format(time.RFC3339Nano)); e != nil {
		return BillingUsageSnapshot{}, e
	}
	high, e := s.db.StorageHighWater(ctx, accountID, string(period))
	if e != nil {
		return BillingUsageSnapshot{}, e
	}
	egress, e := s.db.AccountEgress(ctx, accountID, string(period))
	if e != nil {
		return BillingUsageSnapshot{}, e
	}
	return BillingUsageSnapshot{AccountID: accountID, PlanCode: "nano", Period: period, CurrentStorageBytes: usage.StoredBytes, StorageHighWaterBytes: high, MonthlyEgressBytes: egress}, nil
}
func (s *server) observeAccountStorage(ctx context.Context, repositoryID string, now time.Time) error {
	repo, e := s.db.Repository(ctx, repositoryID)
	if e != nil {
		return e
	}
	usage, e := s.accountUsage(ctx, repo.AccountID)
	if e != nil {
		return e
	}
	return s.db.ObserveStorageHighWater(ctx, repo.AccountID, now.UTC().Format("2006-01"), usage.StoredBytes, now.UTC().Format(time.RFC3339Nano))
}

func (s *server) billingProvider() BillingProvider {
	return MockBillingProvider{Store: s.db, Now: s.currentTime}
}
