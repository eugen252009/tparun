package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
)

const (
	ProfileSelfHosted = "self-hosted"
	ProfileLegacy     = "legacy"
	BillingNone       = "none"
	BillingHosted     = "hosted"
	BillingSimulator  = "simulator"
	ProfileFile       = "server-profile.json"
)

type serverProfile struct {
	Version int    `json:"version"`
	Profile string `json:"profile"`
}

// LoadServer adds explicit initialized-profile defaults to the existing env
// configuration. It never guesses freshness from database existence/version.
// Without a profile, legacy defaults remain byte-for-byte the same.
func LoadServer() (Config, error) {
	c := Load()
	if c.Profile == "" {
		b, err := os.ReadFile(filepath.Join(c.DataDir, ProfileFile))
		if err != nil && !os.IsNotExist(err) {
			return Config{}, fmt.Errorf("read server profile: %w", err)
		}
		if err == nil {
			var p serverProfile
			d := json.NewDecoder(bytes.NewReader(b))
			d.DisallowUnknownFields()
			if err = d.Decode(&p); err != nil {
				return Config{}, fmt.Errorf("invalid server profile: %w", err)
			}
			var extra any
			if d.Decode(&extra) != io.EOF || p.Version != 1 || p.Profile == "" {
				return Config{}, fmt.Errorf("invalid server profile version/content")
			}
			c.Profile = p.Profile
		}
	}
	switch c.Profile {
	case "", ProfileLegacy:
		c.Profile = ProfileLegacy
	case ProfileSelfHosted:
		if CanonicalEnv("TPARUN_DELIVERY_MODE") == "" {
			c.DeliveryMode = DeliveryMetricsOnly
		}
		if c.BillingIntegration == "" && CanonicalEnv("TPARUN_BILLING_PROVIDER") == "" {
			c.BillingIntegration = BillingNone
			c.BillingProvider = "none"
		}
		if !HasCanonicalEnv("TPARUN_LOCAL_PROVISIONING_ENABLED") {
			c.LocalProvisioningEnabled = true
		}
		if c.CapabilitySource == "" {
			c.CapabilitySource = CapabilitiesHosted
			if c.LocalProvisioningEnabled {
				c.CapabilitySource = CapabilitiesLocal
			}
		}
		host, port, _ := net.SplitHostPort(c.Listen)
		if !HasCanonicalEnv("TPARUN_REGISTRATION_ENABLED") {
			c.RegistrationEnabled = host == "localhost" || net.ParseIP(host).IsLoopback()
		}
		if CanonicalEnv("TPARUN_OPERATOR_RP_ID") == "" {
			c.OperatorRelyingPartyID = "localhost"
		}
		if CanonicalEnv("TPARUN_OPERATOR_ORIGIN") == "" {
			c.OperatorOrigin = "http://localhost:" + port
		}
	default:
		return Config{}, fmt.Errorf("unknown server profile %q", c.Profile)
	}
	if err := loadLocalPolicy(&c); err != nil {
		return Config{}, err
	}
	if err := ValidateProvisioning(c); err != nil {
		return Config{}, err
	}
	if _, err := ResolveEgressEnforcement(c.DeliveryMode, c.EgressEnforcement); err != nil {
		return Config{}, err
	}
	if _, err := ResolveBillingMode(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// InitializeSelfHosted is called while the Hub data-directory lock is held.
// It refuses existing state rather than reinterpreting a legacy installation.
// The file stores only the selected profile, not duplicated resolved defaults.
func InitializeSelfHosted(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".tpahub.lock" {
			return fmt.Errorf("initialization requires an empty data directory; found %s", entry.Name())
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, ProfileFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err = json.NewEncoder(f).Encode(serverProfile{Version: 1, Profile: ProfileSelfHosted}); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Explicit commercial integration is independent of payment backend. Empty
// retains legacy provider selection, including mock as simulator (NOT none).
func ResolveBillingMode(c Config) (string, error) {
	if c.BillingIntegration != "" {
		switch c.BillingIntegration {
		case BillingNone, BillingHosted, BillingSimulator:
			return c.BillingIntegration, nil
		}
		return "", fmt.Errorf("unsupported TPARUN_BILLING %q (remote transport is not implemented)", c.BillingIntegration)
	}
	switch c.BillingProvider {
	case "none":
		return BillingNone, nil
	case "stripe":
		return BillingHosted, nil
	case "mock", "":
		return BillingSimulator, nil
	}
	return "", fmt.Errorf("unknown legacy billing provider %q", c.BillingProvider)
}
