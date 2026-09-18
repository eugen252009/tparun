package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func bootstrapOperator(r *http.Request) string {
	_ = r
	if v := strings.TrimSpace(getenv("TPAHUB_OPERATOR")); v != "" {
		return v
	}
	return "operator"
}

func getenv(name string) string { return os.Getenv(name) }

func (s *server) bootstrapRoute(w http.ResponseWriter, r *http.Request) {
	operatorSecurityHeaders(w)
	if !s.operatorAuthorized(r) {
		http.Redirect(w, r, "/ops/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodGet {
		s.bootstrapPage(w, r)
		return
	}
	if r.Method != http.MethodPost || !s.exactOperatorOrigin(r) {
		bad(w, http.StatusForbidden, "invalid request")
		return
	}
	now := s.currentTime().UTC().Format(time.RFC3339Nano)
	if err := r.ParseForm(); err != nil {
		bad(w, 400, "invalid form")
		return
	}
	var err error
	switch r.FormValue("action") {
	case "enable":
		err = s.db.EnableGlobalBootstrap(r.Context(), r.FormValue("repository_id"), bootstrapOperator(r), now)
	case "disable":
		err = s.db.DisableGlobalBootstrap(r.Context(), bootstrapOperator(r), now)
	case "capacity":
		err = s.changeBootstrapCapacity(r, r.FormValue("storage_limit"), r.FormValue("egress_limit"), now)
	default:
		err = fmt.Errorf("unknown bootstrap action")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/ops/bootstrap", http.StatusSeeOther)
}

func (s *server) bootstrapPage(w http.ResponseWriter, r *http.Request) {
	b, found, err := s.db.GlobalBootstrapRepository(r.Context())
	if err != nil {
		bad(w, 500, "bootstrap unavailable")
		return
	}
	data := struct {
		Found                                                           bool
		RepositoryID, AccountID                                         string
		StorageUsed, StorageLimit, Recommended, EgressUsed, EgressLimit string
	}{Found: found, RepositoryID: b.RepositoryID, AccountID: b.AccountID}
	if found {
		storage, egress, err := s.bootstrapLimits(r, b.AccountID)
		if err != nil {
			bad(w, 500, "capacity unavailable")
			return
		}
		usage, _ := s.accountUsage(r.Context(), b.AccountID)
		_, egressUsed, _ := s.accountEgress(b.AccountID, s.currentTime())
		data.StorageUsed = formatBytes(usage.StoredBytes)
		data.StorageLimit = formatBytes(storage)
		data.Recommended = formatBytes(recommendedCapacity(usage.StoredBytes, storage))
		data.EgressUsed = formatBytes(egressUsed)
		data.EgressLimit = formatBytes(egress)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = websiteTemplates.ExecuteTemplate(w, "ops-bootstrap.html", data)
}

func (s *server) bootstrapLimits(r *http.Request, account string) (int64, int64, error) {
	now := s.currentTime()
	storage, err := s.grantLimit(r.Context(), account, commerce.CapabilityID(CapabilityRepositoryPublish), now)
	if err != nil {
		return 0, 0, err
	}
	egress, err := s.grantLimit(r.Context(), account, commerce.CapabilityID(CapabilityRepositoryDownload), now)
	return storage, egress, err
}
func (s *server) grantLimit(ctx context.Context, account string, capability commerce.CapabilityID, now time.Time) (int64, error) {
	grant, err := s.grantService().GetGrant(ctx, commerce.CustomerID(account), tpahubproduct.ProductID, capability, now)
	if err != nil {
		return 0, err
	}
	return grant.Limits.StorageBytes + grant.Limits.EgressBytes, nil
}

func (s *server) changeBootstrapCapacity(r *http.Request, storageText, egressText, now string) error {
	b, found, err := s.db.GlobalBootstrapRepository(r.Context())
	if err != nil {
		return err
	}
	if !found {
		return errors.New("no global bootstrap repository")
	}
	storage, err := parseFiniteCapacity(storageText)
	if err != nil {
		return fmt.Errorf("storage limit: %w", err)
	}
	egress, err := parseFiniteCapacity(egressText)
	if err != nil {
		return fmt.Errorf("egress limit: %w", err)
	}
	usage, err := s.accountUsage(r.Context(), b.AccountID)
	if err != nil {
		return err
	}
	if usage.StoredBytes > storage {
		return fmt.Errorf("storage limit below current usage (%d)", usage.StoredBytes)
	}
	_, usedEgress, err := s.accountEgress(b.AccountID, s.currentTime())
	if err == nil && usedEgress > egress {
		return fmt.Errorf("egress limit below current usage (%d)", usedEgress)
	}
	return s.db.SetBootstrapCapacity(r.Context(), b.AccountID, b.RepositoryID, bootstrapOperator(r), storage, egress, "operator-selected finite capacity", s.currentTime().UTC())
}

func parseFiniteCapacity(v string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("must be a positive finite integer")
	}
	return n, nil
}

func recommendedCapacity(usage, current int64) int64 {
	if usage < 0 || current < 0 {
		return 0
	}
	const unit int64 = 1_000_000_000
	addition := usage / 5
	maxInt := int64(^uint64(0) >> 1)
	target := maxInt
	if usage <= maxInt-addition {
		target = usage + addition
	}
	if target < unit {
		target = unit
	}
	if target < current {
		target = current
	}
	units := target / unit
	if target%unit != 0 && units < maxInt {
		units++
	}
	power := int64(1)
	for power < units && power < (1<<62) {
		power *= 2
	}
	if power > maxInt/unit {
		return maxInt
	}
	return power * unit
}
