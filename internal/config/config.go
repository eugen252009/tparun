package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Listen              string
	DataDir             string
	TPABin              string
	GPGHome             string
	GPGFingerprint      string
	PublishTimeout      time.Duration
	MaxUpload           int64
	RegistrationEnabled bool
	BillingProvider     string
	BillingEnvironment  string
	StripeTestSecret    string
}

func Load() Config {
	return Config{
		Listen:              env("TPAHUB_LISTEN", "127.0.0.1:8080"),
		DataDir:             env("TPAHUB_DATA_DIR", "./data"),
		TPABin:              env("TPA_BIN", "tpa"),
		GPGHome:             os.Getenv("GNUPGHOME"),
		GPGFingerprint:      os.Getenv("TPAHUB_GPG_FINGERPRINT"),
		PublishTimeout:      duration("TPAHUB_PUBLISH_TIMEOUT", 10*time.Minute),
		MaxUpload:           integer("TPAHUB_MAX_UPLOAD_BYTES", 512<<20),
		RegistrationEnabled: os.Getenv("TPAHUB_REGISTRATION_ENABLED") != "false",
		BillingProvider:     env("TPAHUB_BILLING_PROVIDER", "mock"),
		BillingEnvironment:  env("TPAHUB_BILLING_ENVIRONMENT", "mock"),
		StripeTestSecret:    os.Getenv("TPAHUB_STRIPE_TEST_SECRET"),
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
