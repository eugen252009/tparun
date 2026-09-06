package main

import (
	"context"
	"fmt"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

// productionCommerceCatalog is the application composition of the products
// that may be sold. Product definitions remain owned by their product
// packages; Commerce only performs generic admission against this catalog.
type productionCommerceCatalog struct{}

func (productionCommerceCatalog) Product(_ context.Context, id commerce.ProductID) (commerce.Product, error) {
	if id != tpahubproduct.ProductID {
		return commerce.Product{}, fmt.Errorf("product is not registered")
	}
	return commerce.Product{ID: tpahubproduct.ProductID, Name: "TPA Hub", Description: "TPA Hub repository hosting"}, nil
}

func (productionCommerceCatalog) Offer(_ context.Context, product commerce.ProductID, offerID, version string) (commerce.Offer, error) {
	if product != tpahubproduct.ProductID || offerID != tpahubproduct.NanoOffer().ID {
		return commerce.Offer{}, commerce.ErrUnknownOffer
	}
	offer := tpahubproduct.NanoOffer()
	if version != "" && version != offer.Version {
		return commerce.Offer{}, commerce.ErrUnsupportedOfferVersion
	}
	return offer, nil
}

func (s *server) commerceCatalog() commerce.ProductCatalog { return productionCommerceCatalog{} }

// persistAdmittedPurchase is the only application bridge from Commerce
// admission to Store purchase persistence. Callers provide purchase metadata
// such as payment environment, but never grant definitions.
func (s *server) persistAdmittedPurchase(ctx context.Context, catalog commerce.ProductCatalog, request commerce.PurchaseRequest, purchase store.BillingPurchase, now string) (store.BillingPurchase, error) {
	offer, err := commerce.AdmitPurchase(ctx, catalog, request)
	if err != nil {
		return store.BillingPurchase{}, err
	}
	purchase.AccountID = string(request.CustomerID)
	purchase.ProductID = string(request.ProductID)
	purchase.PlanCode = offer.ID
	purchase.OfferVersion = offer.Version
	purchase.AmountMinor = offer.AmountMinor
	purchase.Currency = offer.Currency
	return s.db.CreatePendingPurchase(ctx, purchase, now, offer.Grants...)
}
