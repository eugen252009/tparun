package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
	"math"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
)

type BillingPeriod string

type BillingUsageSnapshot struct {
	AccountID             string        `json:"account_id"`
	ProductID             string        `json:"product_id"`
	PlanCode              string        `json:"plan_code"`
	Period                BillingPeriod `json:"period"`
	CurrentStorageBytes   int64         `json:"current_storage_bytes"`
	StorageHighWaterBytes int64         `json:"storage_high_water_bytes"`
	MonthlyEgressBytes    int64         `json:"monthly_egress_bytes,omitempty"`
	EgressBytes           int64         `json:"egress_bytes"`
}

type RateCardMode string

const (
	RateCardMock       RateCardMode = "mock"
	RateCardProduction RateCardMode = "production"
)

type RateCard struct {
	ID                      string
	Mode                    RateCardMode
	PlanCode                string
	BillingCadence          PlanCadence
	Currency                string
	FixedPlanAmountsMinor   map[string]int64
	ProBaseAmountMinor      int64
	StorageBlockAmountMinor int64
	EgressBlockAmountMinor  int64
}

var mockRateCardV1 = RateCard{ID: "mock-rate-card-v1", Mode: RateCardMock, Currency: "EUR", FixedPlanAmountsMinor: map[string]int64{"nano": 440, "standard": 2200}, ProBaseAmountMinor: 500, StorageBlockAmountMinor: 100, EgressBlockAmountMinor: 200}

type BillingLineItem struct {
	Kind            string `json:"kind"`
	Quantity        int64  `json:"quantity"`
	UnitAmountMinor int64  `json:"unit_amount_minor"`
	AmountMinor     int64  `json:"amount_minor"`
	Currency        string `json:"currency"`
}

type BillingPreview struct {
	AccountID             string            `json:"account_id"`
	ProductID             string            `json:"product_id"`
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

type FinalizedBillingPeriod struct {
	BillingPreview
	RateCardMode   RateCardMode `json:"rate_card_mode"`
	FinalizedAt    time.Time    `json:"finalized_at"`
	IdempotencyKey string       `json:"idempotency_key"`
}

type ProviderSubmissionResult struct {
	Provider          string             `json:"provider"`
	Environment       BillingEnvironment `json:"environment"`
	ExternalReference string             `json:"external_reference"`
	Status            string             `json:"status"`
}

type BillingProvider interface {
	EnvironmentName() BillingEnvironment
	Submit(context.Context, FinalizedBillingPeriod) (ProviderSubmissionResult, error)
}

type MockBillingProvider struct {
	Store    *store.Store
	Now      func() time.Time
	RateCard RateCard
}

func productionNanoRateCard() RateCard {
	return RateCard{ID: "production-nano-v1", Mode: RateCardProduction, PlanCode: "nano", BillingCadence: CadenceAnnual, Currency: "EUR", FixedPlanAmountsMinor: map[string]int64{"nano": tpahubproduct.NanoOffer().AmountMinor}}
}

var productionNanoRateCardV1 = productionNanoRateCard()

func rateCardByID(id string) (RateCard, error) {
	switch id {
	case mockRateCardV1.ID:
		card := mockRateCardV1
		card.FixedPlanAmountsMinor = map[string]int64{"nano": 440, "standard": 2200}
		return card, nil
	case "production-nano-v1":
		return productionNanoRateCard(), nil
	default:
		return RateCard{}, fmt.Errorf("rate card not found: %s", id)
	}
}
func finalizeBillingWithRateCard(snapshot BillingUsageSnapshot, card RateCard, now time.Time) (FinalizedBillingPeriod, error) {
	result, e := rateBillingWithRateCard(snapshot, card)
	if e != nil {
		return FinalizedBillingPeriod{}, e
	}
	return FinalizedBillingPeriod{BillingPreview: result, RateCardMode: card.Mode, FinalizedAt: now.UTC(), IdempotencyKey: fmt.Sprintf("commerce:%s:%s:%s:final", result.ProductID, snapshot.AccountID, snapshot.Period)}, nil
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
	if snapshot.ProductID == "" {
		snapshot.ProductID = string(TPAHubProductID)
	}
	if payload, e := m.Store.ProductMockBillingEventPayload(ctx, snapshot.AccountID, snapshot.ProductID, string(snapshot.Period), "period-finalized"); e == nil {
		var stable FinalizedBillingPeriod
		if json.Unmarshal([]byte(payload), &stable) == nil && stable.RateCardID != "" {
			return stable.BillingPreview, nil
		}
	}
	result, e := rateBillingWithRateCard(snapshot, card)
	if e != nil {
		return BillingPreview{}, e
	}
	now := time.Now().UTC()
	if m.Now != nil {
		now = m.Now().UTC()
	}
	finalized := FinalizedBillingPeriod{BillingPreview: result, RateCardMode: card.Mode, FinalizedAt: now, IdempotencyKey: fmt.Sprintf("commerce:%s:%s:%s:final", result.ProductID, snapshot.AccountID, snapshot.Period)}
	if e = m.recordFinalized(ctx, snapshot, finalized); e != nil {
		return BillingPreview{}, e
	}
	if payload, e := m.Store.ProductMockBillingEventPayload(ctx, snapshot.AccountID, snapshot.ProductID, string(snapshot.Period), "period-finalized"); e == nil {
		var stable FinalizedBillingPeriod
		if json.Unmarshal([]byte(payload), &stable) == nil {
			return stable.BillingPreview, nil
		}
	}
	return result, nil
}
func (m MockBillingProvider) EnvironmentName() BillingEnvironment { return BillingEnvironmentMock }
func (m MockBillingProvider) Submit(ctx context.Context, finalized FinalizedBillingPeriod) (ProviderSubmissionResult, error) {
	if err := validateProviderCompatibility(finalized.RateCardMode, m.EnvironmentName()); err != nil {
		return ProviderSubmissionResult{}, err
	}
	if err := validateFinalized(finalized); err != nil {
		return ProviderSubmissionResult{}, err
	}
	return ProviderSubmissionResult{Provider: "mock", Environment: m.EnvironmentName(), ExternalReference: "mock:" + finalized.IdempotencyKey, Status: "submitted"}, nil
}

func (m MockBillingProvider) recordFinalized(ctx context.Context, s BillingUsageSnapshot, finalized FinalizedBillingPeriod) error {
	if m.Store == nil {
		return fmt.Errorf("mock billing store unavailable")
	}
	payload, e := json.Marshal(finalized)
	if e != nil {
		return e
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s:period-finalized", s.ProductID, s.AccountID, s.Period)))
	now := time.Now().UTC()
	if m.Now != nil {
		now = m.Now().UTC()
	}
	return m.Store.RecordProductMockBillingEvent(ctx, hex.EncodeToString(h[:]), s.AccountID, s.ProductID, string(s.Period), "period-finalized", string(payload), now.Format(time.RFC3339Nano))
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
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s:%s", s.ProductID, s.AccountID, s.Period, event)))
	return m.Store.RecordProductMockBillingEvent(ctx, hex.EncodeToString(h[:]), s.AccountID, s.ProductID, string(s.Period), event, string(payload), now.Format(time.RFC3339Nano))
}

func rateBilling(s BillingUsageSnapshot) BillingPreview {
	result, _ := rateBillingWithRateCard(s, mockRateCardV1)
	return result
}
func rateBillingWithRateCard(s BillingUsageSnapshot, card RateCard) (BillingPreview, error) {
	if s.ProductID == "" {
		s.ProductID = string(TPAHubProductID)
	}
	if err := validateRateCard(card); err != nil {
		return BillingPreview{}, err
	}
	p, ok := planCatalog[s.PlanCode]
	if !ok {
		return BillingPreview{}, fmt.Errorf("unknown plan: %s", s.PlanCode)
	}
	egressBytes := s.MonthlyEgressBytes
	if s.EgressBytes != 0 || (p.EgressCadence == CadenceAnnual && s.MonthlyEgressBytes == 0) {
		egressBytes = s.EgressBytes
	}
	result := BillingPreview{AccountID: s.AccountID, ProductID: s.ProductID, PlanCode: s.PlanCode, Period: s.Period, RateCardID: card.ID, StorageHighWaterBytes: s.StorageHighWaterBytes, EgressBytes: egressBytes, Currency: card.Currency, IsVirtual: true}
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
	result.EgressBlocks = ceilBlocks(egressBytes, p.EgressBlockBytes)
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
	if c.ID == "" || (c.Mode != RateCardMock && c.Mode != RateCardProduction) || c.Currency == "" || c.ProBaseAmountMinor < 0 || c.StorageBlockAmountMinor < 0 || c.EgressBlockAmountMinor < 0 {
		return fmt.Errorf("invalid rate card")
	}
	for _, amount := range c.FixedPlanAmountsMinor {
		if amount < 0 {
			return fmt.Errorf("invalid rate card")
		}
	}
	if c.Mode == RateCardProduction {
		offer := tpahubproduct.NanoOffer()
		if c.ID != "production-nano-v1" || c.PlanCode != offer.ID || c.Currency != offer.Currency || c.BillingCadence != CadenceAnnual || c.FixedPlanAmountsMinor[offer.ID] != offer.AmountMinor || len(c.FixedPlanAmountsMinor) != 1 {
			return fmt.Errorf("invalid production Nano rate card")
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
	return (value-1)/block + 1
}

func (s *server) billingSnapshot(ctx context.Context, accountID string, now time.Time) (BillingUsageSnapshot, error) {
	grant, err := s.grantService().GetGrant(ctx, commerce.CustomerID(accountID), tpahubproduct.ProductID, CapabilityRepositoryDownload, now)
	if err != nil {
		return BillingUsageSnapshot{}, err
	}
	periodStart, periodEnd, err := grant.PeriodAt(now)
	if err != nil {
		return BillingUsageSnapshot{}, err
	}
	period := BillingPeriod(now.UTC().Format("2006-01"))
	if grant.Period == commerce.PeriodAnnual {
		period = BillingPeriod(periodStart.Format("2006-01-02") + "/" + periodEnd.Format("2006-01-02"))
	}
	usage, err := s.accountUsage(ctx, accountID)
	if err != nil {
		return BillingUsageSnapshot{}, err
	}
	// Fixed annual offers do not depend on high-water, so retain the existing
	// monthly operational observation used by future monthly usage pricing.
	highWaterPeriod := string(period)
	if grant.Period == commerce.PeriodAnnual {
		highWaterPeriod = now.UTC().Format("2006-01")
	}
	if err = s.db.ObserveStorageHighWater(ctx, accountID, highWaterPeriod, usage.StoredBytes, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return BillingUsageSnapshot{}, err
	}
	high, err := s.db.StorageHighWater(ctx, accountID, highWaterPeriod)
	if err != nil {
		return BillingUsageSnapshot{}, err
	}
	egress, err := s.db.AccountEgressWindow(ctx, accountID, periodStart, periodEnd)
	if err != nil {
		return BillingUsageSnapshot{}, err
	}
	return BillingUsageSnapshot{AccountID: accountID, ProductID: string(tpahubproduct.ProductID), PlanCode: grant.OfferID, Period: period, CurrentStorageBytes: usage.StoredBytes, StorageHighWaterBytes: high, MonthlyEgressBytes: egress, EgressBytes: egress}, nil
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
func (s *server) billingProvider() MockBillingProvider {
	return MockBillingProvider{Store: s.db, Now: s.currentTime, RateCard: mockRateCardV1}
}
