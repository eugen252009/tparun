package config

import "testing"

func TestEgressEnforcementSelectionAndCompatibility(t *testing.T) {
	for _, tc := range []struct{ delivery, selected, want string }{
		{"", "", EnforcementStrictAccounted},
		{DeliveryStrictAccounted, "", EnforcementStrictAccounted},
		{DeliveryMetricsOnly, "", EnforcementNone},
		{DeliveryUnmetered, "", EnforcementNone},
		{DeliveryStrictAccounted, EnforcementNone, EnforcementNone},
		{DeliveryMetricsOnly, EnforcementStrictAccounted, EnforcementStrictAccounted},
	} {
		got, err := ResolveEgressEnforcement(tc.delivery, tc.selected)
		if err != nil || got != tc.want {
			t.Fatalf("%q/%q => %q %v", tc.delivery, tc.selected, got, err)
		}
	}
	for _, tc := range [][2]string{{DeliveryUnmetered, EnforcementStrictAccounted}, {DeliveryMetricsOnly, "local-quota"}, {DeliveryMetricsOnly, "throttle"}, {DeliveryMetricsOnly, "remote"}, {DeliveryMetricsOnly, "NONE"}, {"invalid", EnforcementNone}} {
		if _, err := ResolveEgressEnforcement(tc[0], tc[1]); err == nil {
			t.Fatalf("unsupported composition accepted: %v", tc)
		}
	}
	t.Setenv("TPAHUB_EGRESS_ENFORCEMENT", EnforcementNone)
	if got := Load().EgressEnforcement; got != EnforcementNone {
		t.Fatalf("environment selection=%q", got)
	}
}

func TestSelfHostedEnforcementDefaultsAndExplicitOverride(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("TPAHUB_PROFILE", ProfileSelfHosted)
	t.Setenv("TPAHUB_EGRESS_ENFORCEMENT", "")
	c, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := ResolveEgressEnforcement(c.DeliveryMode, c.EgressEnforcement)
	if err != nil || selected != EnforcementNone || c.BillingIntegration != BillingNone {
		t.Fatalf("self-hosted enforcement=%q %v", selected, err)
	}
	t.Setenv("TPAHUB_EGRESS_ENFORCEMENT", EnforcementStrictAccounted)
	c, err = LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if c.DeliveryMode != DeliveryMetricsOnly || c.EgressEnforcement != EnforcementStrictAccounted || c.BillingIntegration != BillingNone {
		t.Fatal("explicit local enforcement changed measurement or billing selection")
	}
	t.Setenv("TPAHUB_DELIVERY_MODE", DeliveryUnmetered)
	if _, err := LoadServer(); err == nil {
		t.Fatal("profile accepted strict accounting without measurement")
	}
}
