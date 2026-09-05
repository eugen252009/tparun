package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Listen                   string
	DataDir                  string
	TPABin                   string
	GPGHome                  string
	GPGFingerprint           string
	PublishTimeout           time.Duration
	MaxUpload                int64
	MaxStagingBytes          int64
	MaxAccountStagingBytes   int64
	MaxCandidateInodes       int64
	MinDiskFreeBytes         int64
	MinFreeInodes            int64
	MaxGenerations           int64
	RegistrationEnabled      bool
	LocalProvisioningEnabled bool
	BillingProvider          string
	BillingEnvironment       string
	BillingRateCard          string
	StripeTestSecret         string
	StripeWebhookSecret      string
	BillingPurchasesEnabled  bool
}

func Load() Config {
	return Config{
		Listen:                   env("TPAHUB_LISTEN", "127.0.0.1:8080"),
		DataDir:                  env("TPAHUB_DATA_DIR", "./data"),
		TPABin:                   env("TPA_BIN", "tpa"),
		GPGHome:                  os.Getenv("GNUPGHOME"),
		GPGFingerprint:           os.Getenv("TPAHUB_GPG_FINGERPRINT"),
		PublishTimeout:           duration("TPAHUB_PUBLISH_TIMEOUT", 10*time.Minute),
		MaxUpload:                integer("TPAHUB_MAX_UPLOAD_BYTES", 512<<20),
		MaxStagingBytes:          integer("TPAHUB_MAX_STAGING_BYTES", 4<<30),
		MaxAccountStagingBytes:   integer("TPAHUB_MAX_ACCOUNT_STAGING_BYTES", 1<<30),
		MaxCandidateInodes:       integer("TPAHUB_MAX_CANDIDATE_INODES", 65536),
		MinDiskFreeBytes:         integer("TPAHUB_MIN_DISK_FREE_BYTES", 100<<20),
		MinFreeInodes:            integer("TPAHUB_MIN_FREE_INODES", 1024),
		MaxGenerations:           integer("TPAHUB_MAX_GENERATIONS", 5),
		RegistrationEnabled:      os.Getenv("TPAHUB_REGISTRATION_ENABLED") == "true",
		LocalProvisioningEnabled: os.Getenv("TPAHUB_LOCAL_PROVISIONING_ENABLED") == "true",
		BillingProvider:          env("TPAHUB_BILLING_PROVIDER", "mock"),
		BillingEnvironment:       env("TPAHUB_BILLING_ENVIRONMENT", "mock"),
		BillingRateCard:          env("TPAHUB_BILLING_RATE_CARD", "mock-rate-card-v1"),
		StripeTestSecret:         os.Getenv("TPAHUB_STRIPE_TEST_SECRET"),
		StripeWebhookSecret:      os.Getenv("TPAHUB_STRIPE_WEBHOOK_SECRET"),
		BillingPurchasesEnabled:  os.Getenv("TPAHUB_BILLING_PURCHASES_ENABLED") == "true",
	}
}
func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func duration(k string, d time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if x, e := time.ParseDuration(v); e == nil && x > 0 {
			return x
		}
	}
	return d
}
func integer(k string, d int64) int64 {
	if v := os.Getenv(k); v != "" {
		if x, e := strconv.ParseInt(v, 10, 64); e == nil && x > 0 {
			return x
		}
	}
	return d
}
