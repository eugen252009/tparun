// Package tpahubcapability defines stable application capability identities,
// independent of the policy that provisions them.
package tpahubcapability

import "github.com/eugen252009/tpahub/internal/commerce"

const (
	ProductID             commerce.ProductID    = "tpa-hub"
	RepositoryCreate      commerce.CapabilityID = "repository.create"
	RepositoryPublish     commerce.CapabilityID = "repository.publish"
	RepositoryDownload    commerce.CapabilityID = "repository.download"
	RepositoryReadPrivate commerce.CapabilityID = "repository.read.private"
)
