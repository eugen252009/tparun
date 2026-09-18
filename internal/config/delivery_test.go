package config

import "testing"

func TestDeliveryModeDefaultsAndValidation(t *testing.T) {
	t.Setenv("TPAHUB_DELIVERY_MODE", "")
	if got := Load().DeliveryMode; got != DeliveryStrictAccounted {
		t.Fatalf("default=%q", got)
	}
	for _, mode := range []string{"", DeliveryUnmetered, DeliveryMetricsOnly, DeliveryStrictAccounted} {
		if _, err := ResolveDeliveryMode(mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"reported", "false", "0", "STRICT-ACCOUNTED", "metrics-only "} {
		if _, err := ResolveDeliveryMode(mode); err == nil {
			t.Fatalf("accepted %q", mode)
		}
	}
	t.Setenv("TPAHUB_DELIVERY_MODE", DeliveryUnmetered)
	if got := Load().DeliveryMode; got != DeliveryUnmetered {
		t.Fatalf("mode=%q", got)
	}
}
