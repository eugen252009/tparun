package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/eugen252009/tpahub/internal/provisioning"
)

type Config struct {
	CapabilitySource                      string
	LocalPolicy                           *provisioning.LocalPolicy
	Profile                               string
	BillingIntegration                    string
	CheckoutSuccessURL, CheckoutCancelURL string
	DeliveryMode                          string
	EgressEnforcement                     string
	Listen                                string
	DataDir                               string
	TPABin                                string
	GPGHome                               string
	GPGFingerprint                        string
	PublishTimeout                        time.Duration
	UploadReadTimeout                     time.Duration
	ShutdownTimeout                       time.Duration
	MaxUpload                             int64
	MaxStagingBytes                       int64
	MaxAccountStagingBytes                int64
	MaxCandidateInodes                    int64
	MinDiskFreeBytes                      int64
	MinFreeInodes                         int64
	MaxGenerations                        int64
	RegistrationEnabled                   bool
	LocalProvisioningEnabled              bool
	BillingProvider                       string
	BillingEnvironment                    string
	BillingRateCard                       string
	StripeTestSecret                      string
	StripeWebhookSecret                   string
	BillingPurchasesEnabled               bool
	OperatorRelyingPartyID                string
	OperatorOrigin                        string
}

func Load() Config {
	return Config{
		CapabilitySource:         CanonicalEnv("TPARUN_CAPABILITY_SOURCE"),
		Profile:                  CanonicalEnv("TPARUN_PROFILE"),
		BillingIntegration:       CanonicalEnv("TPARUN_BILLING"),
		CheckoutSuccessURL:       CanonicalEnv("TPARUN_CHECKOUT_SUCCESS_URL"),
		CheckoutCancelURL:        CanonicalEnv("TPARUN_CHECKOUT_CANCEL_URL"),
		DeliveryMode:             envCompat("TPARUN_DELIVERY_MODE", DeliveryStrictAccounted),
		EgressEnforcement:        CanonicalEnv("TPARUN_EGRESS_ENFORCEMENT"),
		Listen:                   envCompat("TPARUN_LISTEN", "127.0.0.1:8080"),
		DataDir:                  envCompat("TPARUN_DATA_DIR", "./data"),
		TPABin:                   env("TPA_BIN", "tpa"),
		GPGHome:                  os.Getenv("GNUPGHOME"),
		GPGFingerprint:           CanonicalEnv("TPARUN_GPG_FINGERPRINT"),
		PublishTimeout:           durationCompat("TPARUN_PUBLISH_TIMEOUT", 10*time.Minute),
		UploadReadTimeout:        durationCompat("TPARUN_UPLOAD_READ_TIMEOUT", 15*time.Minute),
		ShutdownTimeout:          durationCompat("TPARUN_SHUTDOWN_TIMEOUT", 20*time.Second),
		MaxUpload:                integerCompat("TPARUN_MAX_UPLOAD_BYTES", 512<<20),
		MaxStagingBytes:          integerCompatAllowZero("TPARUN_MAX_STAGING_BYTES", 4<<30),
		MaxAccountStagingBytes:   integerCompatAllowZero("TPARUN_MAX_ACCOUNT_STAGING_BYTES", 1<<30),
		MaxCandidateInodes:       integerCompat("TPARUN_MAX_CANDIDATE_INODES", 65536),
		MinDiskFreeBytes:         integerCompatAllowZero("TPARUN_MIN_DISK_FREE_BYTES", 100<<20),
		MinFreeInodes:            integerCompatAllowZero("TPARUN_MIN_FREE_INODES", 1024),
		MaxGenerations:           integerCompatAllowZero("TPARUN_MAX_GENERATIONS", 5),
		RegistrationEnabled:      CanonicalEnv("TPARUN_REGISTRATION_ENABLED") == "true",
		LocalProvisioningEnabled: CanonicalEnv("TPARUN_LOCAL_PROVISIONING_ENABLED") == "true",
		BillingProvider:          envCompat("TPARUN_BILLING_PROVIDER", "mock"),
		BillingEnvironment:       envCompat("TPARUN_BILLING_ENVIRONMENT", "mock"),
		BillingRateCard:          envCompat("TPARUN_BILLING_RATE_CARD", "mock-rate-card-v1"),
		StripeTestSecret:         CanonicalEnv("TPARUN_STRIPE_TEST_SECRET"),
		StripeWebhookSecret:      CanonicalEnv("TPARUN_STRIPE_WEBHOOK_SECRET"),
		BillingPurchasesEnabled:  CanonicalEnv("TPARUN_BILLING_PURCHASES_ENABLED") == "true",
		OperatorRelyingPartyID:   envCompat("TPARUN_OPERATOR_RP_ID", "tpa.run"),
		OperatorOrigin:           envCompat("TPARUN_OPERATOR_ORIGIN", "https://tpa.run"),
	}
}

const (
	DeliveryUnmetered       = "unmetered"
	DeliveryMetricsOnly     = "metrics-only"
	DeliveryStrictAccounted = "strict-accounted"
)

// Empty preserves the historical strict behavior, including Config literals.
func ResolveDeliveryMode(mode string) (string, error) {
	switch mode {
	case "", DeliveryStrictAccounted:
		return DeliveryStrictAccounted, nil
	case DeliveryUnmetered, DeliveryMetricsOnly:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid TPARUN_DELIVERY_MODE %q", mode)
	}
}

const (
	EnforcementNone            = "none"
	EnforcementStrictAccounted = "strict-accounted"
)

// Delivery modes remain compatibility presets. Explicit enforcement overrides
// their enforcement default, never the selection of measurement. Strict
// accounting without measurement is invalid, not silently repaired.
func ResolveEgressEnforcement(delivery, selected string) (string, error) {
	mode, err := ResolveDeliveryMode(delivery)
	if err != nil {
		return "", err
	}
	if selected == "" {
		selected = EnforcementNone
		if mode == DeliveryStrictAccounted {
			selected = EnforcementStrictAccounted
		}
	}
	switch selected {
	case EnforcementNone:
		return selected, nil
	case EnforcementStrictAccounted:
		if mode == DeliveryUnmetered {
			return "", fmt.Errorf("strict-accounted enforcement requires measured delivery")
		}
		return selected, nil
	default:
		return "", fmt.Errorf("unsupported TPARUN_EGRESS_ENFORCEMENT %q", selected)
	}
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func envCompat(k, fallback string) string {
	if v := CanonicalEnv(k); v != "" {
		return v
	}
	return fallback
}
func durationCompat(k string, d time.Duration) time.Duration {
	if v := CanonicalEnv(k); v != "" {
		if x, e := time.ParseDuration(v); e == nil && x > 0 {
			return x
		}
	}
	return d
}
func integerCompat(k string, d int64) int64 {
	if v := CanonicalEnv(k); v != "" {
		if x, e := strconv.ParseInt(v, 10, 64); e == nil && x > 0 {
			return x
		}
	}
	return d
}

func integerCompatAllowZero(k string, d int64) int64 {
	if v := CanonicalEnv(k); v != "" {
		if x, e := strconv.ParseInt(v, 10, 64); e == nil && x >= 0 {
			return x
		}
	}
	return d
}
