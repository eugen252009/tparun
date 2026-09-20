package config

import (
	"path/filepath"
	"testing"
)

func TestCapabilitySourceProfileAndCompatibility(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("TPARUN_DATA_DIR", filepath.Join(t.TempDir(), "new"))
	t.Setenv("TPARUN_PROFILE", ProfileSelfHosted)
	c, err := LoadServer()
	if err != nil || c.CapabilitySource != CapabilitiesLocal {
		t.Fatalf("fresh config: %+v %v", c, err)
	}
	p, err := ResolveLocalPolicy(c)
	if err != nil || p.StorageBytes != 5_000_000 || p.EgressBytes != 12_000_000_000 || !p.PrivateRead {
		t.Fatalf("default policy: %+v %v", p, err)
	}
	for _, billing := range []string{BillingNone, BillingSimulator, BillingHosted} {
		c.BillingIntegration = billing
		c.BillingPurchasesEnabled = true
		source, err := ResolveCapabilitySource(c)
		if err != nil || source != CapabilitiesLocal {
			t.Fatalf("billing changed source: %s %v", source, err)
		}
	}
	for _, tc := range []struct {
		local, purchases bool
		want             string
	}{
		{false, false, CapabilitiesHosted}, {true, false, CapabilitiesLegacyLocal}, {true, true, CapabilitiesHosted},
	} {
		source, err := ResolveCapabilitySource(Config{LocalProvisioningEnabled: tc.local, BillingPurchasesEnabled: tc.purchases})
		if err != nil || source != tc.want {
			t.Fatalf("legacy selection: %s %v", source, err)
		}
	}
	t.Setenv("TPARUN_CAPABILITY_SOURCE", CapabilitiesHosted)
	c, err = LoadServer()
	if err != nil || c.CapabilitySource != CapabilitiesHosted {
		t.Fatal("explicit source lost", err)
	}
	t.Setenv("TPARUN_CAPABILITY_SOURCE", "")
	t.Setenv("TPARUN_LOCAL_PROVISIONING_ENABLED", "false")
	c, err = LoadServer()
	if err != nil || c.CapabilitySource != CapabilitiesHosted {
		t.Fatal("legacy opt-out lost", err)
	}
}

func TestLocalPolicyConfigurationValidation(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("TPARUN_PROFILE", ProfileSelfHosted)
	t.Setenv("TPARUN_LOCAL_STORAGE_BYTES", "100000000000")
	t.Setenv("TPARUN_LOCAL_EGRESS_BYTES", "0")
	t.Setenv("TPARUN_LOCAL_PRIVATE_READ_ENABLED", "false")
	c, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	p, err := ResolveLocalPolicy(c)
	if err != nil || p.StorageBytes != 100000000000 || p.EgressBytes != 0 || p.PrivateRead {
		t.Fatalf("configured policy: %+v %v", p, err)
	}
	for _, key := range []string{"TPARUN_LOCAL_STORAGE_BYTES", "TPARUN_LOCAL_EGRESS_BYTES"} {
		for _, bad := range []string{"", "-1", "unlimited", "1.5", "9223372036854775807", "9223372036854775808"} {
			t.Run(key+"/"+bad, func(t *testing.T) {
				t.Setenv(key, bad)
				if _, err := LoadServer(); err == nil {
					t.Fatal("invalid limit accepted")
				}
			})
		}
	}
	for _, tc := range []struct{ key, value string }{{"TPARUN_CAPABILITY_SOURCE", "remote"}, {"TPARUN_CAPABILITY_SOURCE", "LOCAL"}, {"TPARUN_LOCAL_PRIVATE_READ_ENABLED", "yes"}, {"TPARUN_LOCAL_PRIVATE_READ_ENABLED", ""}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := LoadServer(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
