package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
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

type RateCard struct {
	ID                      string
	Currency                string
	FixedPlanAmountsMinor   map[string]int64
	ProBaseAmountMinor      int64
	StorageBlockAmountMinor int64
	EgressBlockAmountMinor  int64
}

var mockRateCardV1 = RateCard{ID: "mock-rate-card-v1", Currency: "EUR", FixedPlanAmountsMinor: map[string]int64{"nano": 440, "standard": 2200}, ProBaseAmountMinor: 500, StorageBlockAmountMinor: 100, EgressBlockAmountMinor: 200}

type BillingLineItem struct {
	Kind            string `json:"kind"`
	Quantity        int64  `json:"quantity"`
	UnitAmountMinor int64  `json:"unit_amount_minor"`
	AmountMinor     int64  `json:"amount_minor"`
	Currency        string `json:"currency"`
}

type BillingPreview struct {
	AccountID             string            `json:"account_id"`
	PlanCode              string            `json:"plan_code"`
	Period                BillingPeriod     `json:"period"`
	RateCardID            string            `json:"rate_card_id"`
	StorageHighWaterBytes int64             `json:"storage_high_water_bytes"`
	EgressBytes           int64             `json:"egress_bytes"`
	StorageBlocks         int64             `json:"storage_blocks"`
	EgressBlocks          int64             `json:"egress_blocks"`
	Currency              string            `json:"currency"`
	LineItems             []BillingLineItem `json:"line_items"`
	TotalAmountMinor      int64             `json:"total_amount_minor"`
	IsVirtual             bool              `json:"is_virtual"`
}

type BillingProvider interface {
	Preview(context.Context, BillingUsageSnapshot) (BillingPreview, error)
	Finalize(context.Context, BillingUsageSnapshot) (BillingPreview, error)
}

type MockBillingProvider struct {
	Store    *store.Store
	Now      func() time.Time
	RateCard RateCard
}

func (m MockBillingProvider) card() RateCard {
	if m.RateCard.ID == "" {
		return mockRateCardV1
	}
	return m.RateCard
}
func (m MockBillingProvider) Preview(ctx context.Context, snapshot BillingUsageSnapshot) (BillingPreview, error) {
	card := m.card()
	result, e := rateBillingWithRateCard(snapshot, card)
	if e != nil {
		return BillingPreview{}, e
	}
	return result, m.record(ctx, snapshot, "billing-preview-generated:"+card.ID, result)
}
func (m MockBillingProvider) Finalize(ctx context.Context, snapshot BillingUsageSnapshot) (BillingPreview, error) {
	card := m.card()
	if payload, e := m.Store.MockBillingEventPayload(ctx, snapshot.AccountID, string(snapshot.Period), "period-finalized"); e == nil {
		var result BillingPreview
		if json.Unmarshal([]byte(payload), &result) == nil {
			return result, nil
		}
	}
	result, e := rateBillingWithRateCard(snapshot, card)
	if e != nil {
		return BillingPreview{}, e
	}
	if e = m.record(ctx, snapshot, "period-finalized", result); e != nil {
		return BillingPreview{}, e
	}
	if payload, e := m.Store.MockBillingEventPayload(ctx, snapshot.AccountID, string(snapshot.Period), "period-finalized"); e == nil {
		var stable BillingPreview
		if json.Unmarshal([]byte(payload), &stable) == nil {
			return stable, nil
		}
	}
	return result, nil
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
	result, _ := rateBillingWithRateCard(s, mockRateCardV1)
	return result
}
func rateBillingWithRateCard(s BillingUsageSnapshot, card RateCard) (BillingPreview, error) {
	if err := validateRateCard(card); err != nil {
		return BillingPreview{}, err
	}
	p, ok := planCatalog[s.PlanCode]
	if !ok {
		return BillingPreview{}, fmt.Errorf("unknown plan: %s", s.PlanCode)
	}
	result := BillingPreview{AccountID: s.AccountID, PlanCode: s.PlanCode, Period: s.Period, RateCardID: card.ID, StorageHighWaterBytes: s.StorageHighWaterBytes, EgressBytes: s.MonthlyEgressBytes, Currency: card.Currency, IsVirtual: true}
	if p.BillingMode == BillingFixed {
		amount, ok := card.FixedPlanAmountsMinor[s.PlanCode]
		if !ok {
			return BillingPreview{}, fmt.Errorf("rate card has no fixed amount for plan %s", s.PlanCode)
		}
		result.LineItems = []BillingLineItem{{Kind: "fixed-plan", Quantity: 1, UnitAmountMinor: amount, AmountMinor: amount, Currency: card.Currency}}
		result.TotalAmountMinor = amount
		return result, nil
	}
	result.StorageBlocks = ceilBlocks(s.StorageHighWaterBytes, p.StorageBlockBytes)
	result.EgressBlocks = ceilBlocks(s.MonthlyEgressBytes, p.EgressBlockBytes)
	items := []BillingLineItem{}
	if card.ProBaseAmountMinor > 0 {
		items = append(items, BillingLineItem{Kind: "base", Quantity: 1, UnitAmountMinor: card.ProBaseAmountMinor, AmountMinor: card.ProBaseAmountMinor, Currency: card.Currency})
		result.TotalAmountMinor = card.ProBaseAmountMinor
	}
	var e error
	if result.StorageBlocks > 0 {
		var amount int64
		amount, e = safeMultiply(result.StorageBlocks, card.StorageBlockAmountMinor)
		if e != nil {
			return BillingPreview{}, e
		}
		items = append(items, BillingLineItem{Kind: "storage-block", Quantity: result.StorageBlocks, UnitAmountMinor: card.StorageBlockAmountMinor, AmountMinor: amount, Currency: card.Currency})
		result.TotalAmountMinor, e = safeAdd(result.TotalAmountMinor, amount)
		if e != nil {
			return BillingPreview{}, e
		}
	}
	if result.EgressBlocks > 0 {
		var amount int64
		amount, e = safeMultiply(result.EgressBlocks, card.EgressBlockAmountMinor)
		if e != nil {
			return BillingPreview{}, e
		}
		items = append(items, BillingLineItem{Kind: "egress-block", Quantity: result.EgressBlocks, UnitAmountMinor: card.EgressBlockAmountMinor, AmountMinor: amount, Currency: card.Currency})
		result.TotalAmountMinor, e = safeAdd(result.TotalAmountMinor, amount)
		if e != nil {
			return BillingPreview{}, e
		}
	}
	result.LineItems = items
	return result, nil
}
func validateRateCard(c RateCard) error {
	if c.ID == "" || c.Currency == "" || c.ProBaseAmountMinor < 0 || c.StorageBlockAmountMinor < 0 || c.EgressBlockAmountMinor < 0 {
		return fmt.Errorf("invalid rate card")
	}
	for _, amount := range c.FixedPlanAmountsMinor {
		if amount < 0 {
			return fmt.Errorf("invalid rate card")
		}
	}
	return nil
}
func safeMultiply(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a != 0 && b > math.MaxInt64/a {
		return 0, fmt.Errorf("billing amount overflow")
	}
	return a * b, nil
}
func safeAdd(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, fmt.Errorf("billing amount overflow")
	}
	return a + b, nil
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
	return MockBillingProvider{Store: s.db, Now: s.currentTime, RateCard: mockRateCardV1}
}
