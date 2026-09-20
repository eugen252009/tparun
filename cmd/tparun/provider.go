package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/payments"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

type BillingEnvironment = payments.Environment

const (
	BillingEnvironmentMock       = payments.EnvironmentMock
	BillingEnvironmentTest       = payments.EnvironmentTest
	BillingEnvironmentProduction = payments.EnvironmentProduction
)

type CheckoutResult = payments.CheckoutSession
type PurchaseProvider = payments.Provider
type providerError = payments.Error

func ValidateBillingConfiguration(c config.Config) error {
	mode, err := config.ResolveBillingMode(c)
	if err != nil {
		return err
	}
	source, err := config.ResolveCapabilitySource(c)
	if err != nil {
		return err
	}
	if c.RegistrationEnabled && !c.BillingPurchasesEnabled && source == config.CapabilitiesHosted {
		return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("public registration requires billing purchases or explicit local provisioning")}
	}
	if mode == config.BillingNone {
		if c.BillingPurchasesEnabled {
			return fmt.Errorf("billing none cannot enable purchases")
		}
		return nil
	}
	if mode == config.BillingHosted {
		if c.BillingProvider != "stripe" {
			return fmt.Errorf("unsupported hosted payment adapter %q", c.BillingProvider)
		}
		if _, _, err := hostedCheckoutURLs(c); err != nil {
			return err
		}
	} else if c.BillingProvider != "mock" && c.BillingProvider != "" {
		return fmt.Errorf("simulator requires mock billing provider")
	}
	if c.BillingPurchasesEnabled && c.BillingProvider != "stripe" {
		return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("purchase flow requires Stripe test provider")}
	}
	env := BillingEnvironment(c.BillingEnvironment)
	card, cardErr := rateCardByID(c.BillingRateCard)
	if c.BillingRateCard == "" {
		card, cardErr = mockRateCardV1, nil
	}
	if cardErr != nil || validateRateCard(card) != nil {
		return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("invalid billing rate card: %s", c.BillingRateCard)}
	}
	if err := validateProviderCompatibility(card.Mode, env); err != nil {
		return err
	}
	if c.BillingProvider == "mock" {
		if env != BillingEnvironmentMock || card.Mode != RateCardMock {
			return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("mock provider requires mock billing environment")}
		}
		return nil
	}
	if c.BillingProvider == "stripe" {
		if env != BillingEnvironmentTest {
			return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Stripe is only enabled in test environment")}
		}
		if !strings.HasPrefix(c.StripeTestSecret, "sk_test_") {
			return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Stripe test secret is required")}
		}
		if c.BillingPurchasesEnabled && (c.BillingRateCard != "production-nano-v1" || c.StripeWebhookSecret == "") {
			return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Nano TEST purchases require production-nano-v1 and a webhook secret")}
		}
		return nil
	}
	return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("unknown billing provider: %s", c.BillingProvider)}
}

func validateProviderCompatibility(mode RateCardMode, environment BillingEnvironment) error {
	if mode == RateCardMock && environment == BillingEnvironmentProduction {
		return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("mock rate card cannot be submitted to a production provider")}
	}
	if environment == BillingEnvironmentProduction {
		return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("production billing is disabled until a production rate card exists")}
	}
	if mode == RateCardProduction && environment != BillingEnvironmentTest {
		return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("production rate cards are currently enabled only with the Stripe test provider")}
	}
	return nil
}

func validateFinalized(f FinalizedBillingPeriod) error {
	if f.AccountID == "" || f.Period == "" || f.PlanCode == "" || f.RateCardID == "" || f.RateCardMode == "" || f.Currency == "" || f.IdempotencyKey == "" {
		return providerError{Code: "provider-invalid-request", Cause: fmt.Errorf("incomplete finalized billing result")}
	}
	sum := int64(0)
	for _, item := range f.LineItems {
		if item.Quantity < 0 || item.UnitAmountMinor < 0 || item.AmountMinor < 0 || item.Currency != f.Currency {
			return providerError{Code: "provider-invalid-request", Cause: fmt.Errorf("invalid billing line item")}
		}
		amount, err := safeMultiply(item.Quantity, item.UnitAmountMinor)
		if err != nil || amount != item.AmountMinor {
			return providerError{Code: "provider-invalid-request", Cause: fmt.Errorf("billing line item amount is inconsistent")}
		}
		sum, err = safeAdd(sum, item.AmountMinor)
		if err != nil {
			return providerError{Code: "provider-invalid-request", Cause: err}
		}
	}
	if sum != f.TotalAmountMinor {
		return providerError{Code: "provider-invalid-request", Cause: fmt.Errorf("billing line item sum does not equal total")}
	}
	if f.RateCardMode == RateCardProduction {
		card, cardErr := rateCardByID(f.RateCardID)
		parts := strings.Split(string(f.Period), "/")
		start, startErr := time.Parse("2006-01-02", "")
		end, endErr := time.Parse("2006-01-02", "")
		if len(parts) == 2 {
			start, startErr = time.Parse("2006-01-02", parts[0])
			end, endErr = time.Parse("2006-01-02", parts[1])
		}
		offer := tpahubproduct.NanoOffer()
		expectedEnd, cadenceErr := advanceCadence(start, offer.Cadence)
		if cardErr != nil || validateRateCard(card) != nil || f.PlanCode != offer.ID || f.Currency != offer.Currency || f.TotalAmountMinor != offer.AmountMinor || startErr != nil || endErr != nil || cadenceErr != nil || !end.Equal(expectedEnd) {
			return providerError{Code: "provider-invalid-request", Cause: fmt.Errorf("invalid production Nano finalization")}
		}
	}
	return nil
}
