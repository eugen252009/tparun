package main

import (
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/provisioning"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

// The legacy adapter is deliberately explicit. Existing unprofiled deployments
// keep their original initial grants. New operator-local policy never calls it.
type legacyLocalProvisioner struct{}

func (legacyLocalProvisioner) InitialGrants(created time.Time) []commerce.Grant {
	return tpahubproduct.DefaultGrants(created)
}

func initialGrantSource(c config.Config) (provisioning.Source, error) {
	if err := config.ValidateProvisioning(c); err != nil {
		return nil, err
	}
	source, err := config.ResolveCapabilitySource(c)
	if err != nil {
		return nil, err
	}
	switch source {
	case config.CapabilitiesLocal:
		return config.ResolveLocalPolicy(c)
	case config.CapabilitiesLegacyLocal:
		return legacyLocalProvisioner{}, nil
	default:
		// Hosted grants originate from verified purchases, not account creation.
		// No placeholder commercial service or fabricated grant is constructed.
		return nil, nil
	}
}

func (s *server) initialTPAGrants(createdAt string) ([]commerce.Grant, error) {
	source, err := initialGrantSource(s.cfg)
	if err != nil || source == nil {
		return nil, err
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, err
	}
	return source.InitialGrants(created), nil
}
