package config

import (
	"fmt"
	"strconv"

	"github.com/eugen252009/tpahub/internal/provisioning"
)

const (
	CapabilitiesLocal       = "local"
	CapabilitiesHosted      = "hosted-commercial"
	CapabilitiesLegacyLocal = "legacy-local"
)

// Explicit source selection is independent of billing, delivery and enforcement.
// Only an absent selector retains the old local-provisioning/purchase switches.
func ResolveCapabilitySource(c Config) (string, error) {
	if c.CapabilitySource == "" {
		if c.LocalProvisioningEnabled && !c.BillingPurchasesEnabled {
			return CapabilitiesLegacyLocal, nil
		}
		return CapabilitiesHosted, nil
	}
	switch c.CapabilitySource {
	case CapabilitiesLocal, CapabilitiesHosted, CapabilitiesLegacyLocal:
		return c.CapabilitySource, nil
	default:
		return "", fmt.Errorf("unsupported TPARUN_CAPABILITY_SOURCE %q", c.CapabilitySource)
	}
}

func ResolveLocalPolicy(c Config) (provisioning.LocalPolicy, error) {
	p := provisioning.DefaultLocalPolicy()
	if c.LocalPolicy != nil {
		p = *c.LocalPolicy
	}
	return p, p.Validate()
}

func ValidateProvisioning(c Config) error {
	source, err := ResolveCapabilitySource(c)
	if err != nil {
		return err
	}
	if source == CapabilitiesLocal || c.LocalPolicy != nil {
		_, err = ResolveLocalPolicy(c)
	}
	return err
}

// Unlike legacy resource-bound parsers, invalid new policy settings must not
// silently fall back. Parse even unused explicit settings to catch operator typos.
func loadLocalPolicy(c *Config) error {
	p := provisioning.DefaultLocalPolicy()
	for key, target := range map[string]*int64{
		"TPARUN_LOCAL_STORAGE_BYTES": &p.StorageBytes,
		"TPARUN_LOCAL_EGRESS_BYTES":  &p.EgressBytes,
	} {
		if HasCanonicalEnv(key) {
			v := CanonicalEnv(key)
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid %s: expected finite byte count", key)
			}
			*target = n
		}
	}
	if HasCanonicalEnv("TPARUN_LOCAL_PRIVATE_READ_ENABLED") {
		v := CanonicalEnv("TPARUN_LOCAL_PRIVATE_READ_ENABLED")
		if v != "true" && v != "false" {
			return fmt.Errorf("invalid TPARUN_LOCAL_PRIVATE_READ_ENABLED: expected true or false")
		}
		p.PrivateRead = v == "true"
	}
	if err := p.Validate(); err != nil {
		return err
	}
	c.LocalPolicy = &p
	return nil
}
