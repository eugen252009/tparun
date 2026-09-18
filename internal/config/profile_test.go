package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func clearProfileEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"TPAHUB_CAPABILITY_SOURCE", "TPAHUB_PROFILE", "TPAHUB_DELIVERY_MODE", "TPAHUB_EGRESS_ENFORCEMENT", "TPAHUB_BILLING", "TPAHUB_BILLING_PROVIDER", "TPAHUB_OPERATOR_RP_ID", "TPAHUB_OPERATOR_ORIGIN"} {
		t.Setenv(k, "")
	}
	// Empty boolean envs are explicit false. Remove them and restore on cleanup.
	for _, k := range []string{"TPAHUB_LOCAL_STORAGE_BYTES", "TPAHUB_LOCAL_EGRESS_BYTES", "TPAHUB_LOCAL_PRIVATE_READ_ENABLED", "TPAHUB_LOCAL_PROVISIONING_ENABLED", "TPAHUB_REGISTRATION_ENABLED"} {
		value, exists := os.LookupEnv(k)
		os.Unsetenv(k)
		t.Cleanup(func() {
			if exists {
				os.Setenv(k, value)
			} else {
				os.Unsetenv(k)
			}
		})
	}
	t.Setenv("TPAHUB_LISTEN", "127.0.0.1:8080")
}
func TestInitializedSelfHostedProfileAndLegacyCompatibility(t *testing.T) {
	clearProfileEnv(t)
	dir := t.TempDir()
	t.Setenv("TPAHUB_DATA_DIR", dir)
	legacy, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Profile != ProfileLegacy || legacy.DeliveryMode != DeliveryStrictAccounted || legacy.BillingProvider != "mock" {
		t.Fatalf("legacy profile=%s delivery=%s provider=%s", legacy.Profile, legacy.DeliveryMode, legacy.BillingProvider)
	}
	if err := InitializeSelfHosted(dir); err != nil {
		t.Fatal(err)
	}
	fresh, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	mode, err := ResolveBillingMode(fresh)
	if err != nil || fresh.Profile != ProfileSelfHosted || fresh.DeliveryMode != DeliveryMetricsOnly || mode != BillingNone || !fresh.LocalProvisioningEnabled || !fresh.RegistrationEnabled {
		t.Fatalf("fresh profile not usable: mode=%s error=%v", mode, err)
	}
	info, err := os.Stat(filepath.Join(dir, ProfileFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("profile permissions=%v %v", info, err)
	}
	if err := InitializeSelfHosted(dir); err == nil {
		t.Fatal("initialization overwrote profile")
	}
	t.Setenv("TPAHUB_DELIVERY_MODE", DeliveryStrictAccounted)
	t.Setenv("TPAHUB_BILLING", BillingHosted)
	t.Setenv("TPAHUB_BILLING_PROVIDER", "stripe")
	explicit, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if explicit.DeliveryMode != DeliveryStrictAccounted || explicit.BillingIntegration != BillingHosted {
		t.Fatal("explicit configuration lost")
	}
}
func TestProfileInitializationHonorsPrivateModesUnderPermissiveUmask(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	dir := filepath.Join(t.TempDir(), "nested")
	if err := InitializeSelfHosted(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("data directory mode=%o err=%v", info.Mode().Perm(), err)
	}
	profile, err := os.Stat(filepath.Join(dir, ProfileFile))
	if err != nil || profile.Mode().Perm() != 0600 {
		t.Fatalf("profile mode=%o err=%v", profile.Mode().Perm(), err)
	}
}

func TestProfileInitializationRejectsExistingStateAndMalformedProfiles(t *testing.T) {
	clearProfileEnv(t)
	dir := t.TempDir()
	t.Setenv("TPAHUB_DATA_DIR", dir)
	db := filepath.Join(dir, "tpahub.db")
	if err := os.WriteFile(db, []byte("existing-state"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InitializeSelfHosted(dir); err == nil {
		t.Fatal("existing state reinterpreted")
	}
	if b, err := os.ReadFile(db); err != nil || string(b) != "existing-state" {
		t.Fatal("existing state modified")
	}
	if _, err := os.Stat(filepath.Join(dir, ProfileFile)); !os.IsNotExist(err) {
		t.Fatal("profile created over existing state")
	}
	for _, value := range []string{`{`, `{"version":2,"profile":"self-hosted"}`, `{"version":1,"profile":"unknown"}`, `{"version":1,"profile":"self-hosted","extra":true}`, `{"version":1,"profile":"self-hosted"} {}`} {
		if err := os.WriteFile(filepath.Join(dir, ProfileFile), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadServer(); err == nil {
			t.Fatalf("accepted corrupt profile %q", value)
		}
	}
}
func TestSelfHostedProfileDoesNotOpenPublicRegistrationImplicitly(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("TPAHUB_PROFILE", ProfileSelfHosted)
	t.Setenv("TPAHUB_LISTEN", "0.0.0.0:8080")
	c, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if c.RegistrationEnabled {
		t.Fatal("public registration enabled implicitly")
	}
	t.Setenv("TPAHUB_REGISTRATION_ENABLED", "true")
	c, err = LoadServer()
	if err != nil || !c.RegistrationEnabled {
		t.Fatal("explicit registration not honored")
	}
}
func TestBillingIntegrationSelection(t *testing.T) {
	for _, tc := range []struct{ integration, provider, want string }{
		{"none", "stripe", BillingNone}, {"hosted", "stripe", BillingHosted}, {"simulator", "mock", BillingSimulator},
		{"", "stripe", BillingHosted}, {"", "mock", BillingSimulator}, {"", "none", BillingNone},
	} {
		got, err := ResolveBillingMode(Config{BillingIntegration: tc.integration, BillingProvider: tc.provider})
		if err != nil || got != tc.want {
			t.Fatalf("selection=%s %v", got, err)
		}
	}
	if _, err := ResolveBillingMode(Config{BillingIntegration: "remote"}); err == nil {
		t.Fatal("unimplemented transport accepted")
	}
}
