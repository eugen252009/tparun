package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

type opsAccountRow struct {
	ID, Class                               string
	Repositories                            int
	StorageBytes, StorageLimit, EgressBytes int64
}
type opsRepositoryRow struct {
	ID, Name, AccountID, Class string
	StorageBytes, EgressBytes  int64
}
type opsPageData struct {
	GeneratedAt, Scope, BillingEnvironment                                                  string
	TotalAccounts, ProductionAccounts, TestAccounts, InternalAccounts, UnclassifiedAccounts int
	TotalRepositories, ProductionRepositories                                               int
	StorageBytes, StorageLimit, EgressBytes, Attention                                      int64
	Pending, Paid, Failed, Expired, Reconciliation                                          int
	Accounts                                                                                []opsAccountRow
	Repositories                                                                            []opsRepositoryRow
	PhysicalCapacity                                                                        string
}

func (s *server) operatorAuthorized(r *http.Request) bool {
	return s.requireOperatorSession(r)
}

func (s *server) ops(w http.ResponseWriter, r *http.Request) {
	operatorSecurityHeaders(w)
	if r.Method != http.MethodGet {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.operatorAuthorized(r) {
		if r.Method == http.MethodGet {
			http.Redirect(w, r, "/ops/login", http.StatusSeeOther)
			return
		}
		bad(w, http.StatusNotFound, "not found")
		return
	}
	ctx := r.Context()
	now := s.currentTime().UTC()
	scope := r.URL.Query().Get("scope")
	if scope != "all" && scope != "test-internal" {
		scope = "production"
	}
	accounts, err := s.db.DashboardAccounts(ctx, now.AddDate(0, 0, -30), now.AddDate(0, 0, 1))
	if err != nil {
		bad(w, 500, "dashboard unavailable")
		return
	}
	repositories, err := s.db.DashboardRepositories(ctx, now.AddDate(0, 0, -30), now.AddDate(0, 0, 1))
	if err != nil {
		bad(w, 500, "dashboard unavailable")
		return
	}
	page := opsPageData{GeneratedAt: now.Format(time.RFC3339), Scope: scope, BillingEnvironment: s.cfg.BillingProvider + " " + strings.ToUpper(s.cfg.BillingEnvironment), PhysicalCapacity: "Unavailable"}
	if s.diskFree != nil {
		if free, freeErr := s.diskFree(); freeErr == nil {
			page.PhysicalCapacity = formatBytes(free) + " free"
		}
	}
	included := func(class string) bool {
		return scope == "all" || (scope == "test-internal" && (class == store.AccountClassTest || class == store.AccountClassInternal)) || (scope == "production" && class == store.AccountClassProduction)
	}
	accountClasses := map[string]struct{}{store.AccountClassProduction: {}, store.AccountClassTest: {}, store.AccountClassInternal: {}, store.AccountClassUnclassified: {}}
	for _, account := range accounts {
		if _, ok := accountClasses[account.AccountClass]; !ok {
			account.AccountClass = store.AccountClassUnclassified
		}
		page.TotalAccounts++
		switch account.AccountClass {
		case store.AccountClassProduction:
			page.ProductionAccounts++
		case store.AccountClassTest:
			page.TestAccounts++
		case store.AccountClassInternal:
			page.InternalAccounts++
		default:
			page.UnclassifiedAccounts++
		}
		if !included(account.AccountClass) {
			continue
		}
		usage, usageErr := s.accountUsage(ctx, account.ID)
		if usageErr != nil {
			bad(w, 500, "dashboard storage unavailable")
			return
		}
		page.TotalRepositories += account.RepositoryCount
		page.StorageBytes += usage.StoredBytes
		page.EgressBytes += account.EgressBytes
		row := opsAccountRow{ID: account.ID, Class: account.AccountClass, Repositories: account.RepositoryCount, StorageBytes: usage.StoredBytes, EgressBytes: account.EgressBytes}
		if grant, grantErr := s.grantService().GetGrant(ctx, commerce.CustomerID(account.ID), tpahubproduct.ProductID, CapabilityRepositoryPublish, now); grantErr == nil {
			row.StorageLimit = grant.Limits.StorageBytes
			page.StorageLimit += grant.Limits.StorageBytes
		}
		page.Accounts = append(page.Accounts, row)
	}
	for _, repository := range repositories {
		accountClass := store.AccountClassUnclassified
		for _, account := range accounts {
			if account.ID == repository.AccountID {
				accountClass = account.AccountClass
				break
			}
		}
		if !included(accountClass) {
			continue
		}
		page.ProductionRepositories++
		stats, statsErr := s.repositoryUsage(repository.ID)
		if statsErr != nil {
			bad(w, 500, "dashboard storage unavailable")
			return
		}
		page.Repositories = append(page.Repositories, opsRepositoryRow{ID: repository.ID, Name: repository.Name, AccountID: repository.AccountID, Class: accountClass, StorageBytes: stats.StoredBytes, EgressBytes: repository.EgressBytes})
	}
	billing, err := s.db.DashboardBillingScoped(ctx, scope)
	if err != nil {
		bad(w, 500, "dashboard billing unavailable")
		return
	}
	page.Pending, page.Paid, page.Failed, page.Expired, page.Reconciliation = billing.Pending, billing.Paid, billing.Failed, billing.Expired, billing.ReconciliationRequired
	attention, err := s.db.DashboardAttentionScoped(ctx, scope)
	if err != nil {
		bad(w, 500, "dashboard attention unavailable")
		return
	}
	page.Attention = int64(attention)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := websiteTemplates.ExecuteTemplate(w, "ops.html", page); err != nil {
		bad(w, 500, fmt.Sprintf("dashboard render failed: %v", err))
	}
}
