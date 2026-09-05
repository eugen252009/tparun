package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type DashboardAccount struct {
	ID, AccountClass string
	RepositoryCount  int
	EgressBytes      int64
}

type DashboardRepository struct {
	ID, AccountID, Name string
	EgressBytes         int64
}

type DashboardBilling struct {
	Pending, Paid, Failed, Expired, ReconciliationRequired int
}

func validAccountClass(class string) bool {
	switch class {
	case AccountClassProduction, AccountClassTest, AccountClassInternal, AccountClassUnclassified:
		return true
	default:
		return false
	}
}

func (s *Store) AllAccounts(c context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(c, `SELECT a.id,a.email,a.password_hash,a.status,a.created_at,COALESCE(m.account_class,?) FROM accounts a LEFT JOIN account_metadata m ON m.account_id=a.id ORDER BY a.id`, AccountClassUnclassified)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Status, &a.CreatedAt, &a.AccountClass); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SetAccountClass(c context.Context, accountID, class string) error {
	if strings.TrimSpace(accountID) == "" || !validAccountClass(class) {
		return fmt.Errorf("invalid account classification")
	}
	result, err := s.db.ExecContext(c, `UPDATE account_metadata SET account_class=? WHERE account_id=?`, class, accountID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DashboardAccounts(c context.Context, start, end time.Time) ([]DashboardAccount, error) {
	rows, err := s.db.QueryContext(c, `SELECT a.id,COALESCE(m.account_class,?),COUNT(DISTINCT r.id),COALESCE((SELECT SUM(u.egress_bytes) FROM repository_egress_daily u JOIN repositories er ON er.id=u.repository_id WHERE er.account_id=a.id AND u.usage_date>=? AND u.usage_date<?),0) FROM accounts a LEFT JOIN account_metadata m ON m.account_id=a.id LEFT JOIN repositories r ON r.account_id=a.id GROUP BY a.id,m.account_class ORDER BY a.id`, AccountClassUnclassified, start.UTC().Format("2006-01-02"), end.UTC().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DashboardAccount
	for rows.Next() {
		var a DashboardAccount
		if err := rows.Scan(&a.ID, &a.AccountClass, &a.RepositoryCount, &a.EgressBytes); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) DashboardRepositories(c context.Context, start, end time.Time) ([]DashboardRepository, error) {
	rows, err := s.db.QueryContext(c, `SELECT r.id,COALESCE(r.account_id,''),r.name,COALESCE((SELECT SUM(u.egress_bytes) FROM repository_egress_daily u WHERE u.repository_id=r.id AND u.usage_date>=? AND u.usage_date<?),0) FROM repositories r ORDER BY r.id LIMIT 100`, start.UTC().Format("2006-01-02"), end.UTC().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DashboardRepository
	for rows.Next() {
		var r DashboardRepository
		if err := rows.Scan(&r.ID, &r.AccountID, &r.Name, &r.EgressBytes); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func dashboardClassFilter(scope, alias string) (string, []any) {
	switch scope {
	case AccountClassProduction:
		return "COALESCE(" + alias + ".account_class,?)=?", []any{AccountClassUnclassified, AccountClassProduction}
	case "test-internal":
		return alias + ".account_class IN (?,?)", []any{AccountClassTest, AccountClassInternal}
	default:
		return "1=1", nil
	}
}

func (s *Store) DashboardBillingScoped(c context.Context, scope string) (DashboardBilling, error) {
	var b DashboardBilling
	filter, args := dashboardClassFilter(scope, "m")
	rows, err := s.db.QueryContext(c, `SELECT p.status,COUNT(*) FROM billing_purchases p LEFT JOIN account_metadata m ON m.account_id=p.account_id WHERE `+filter+` GROUP BY p.status`, args...)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return b, err
		}
		switch status {
		case "pending":
			b.Pending += count
		case "paid":
			b.Paid += count
		case "failed":
			b.Failed += count
		case "expired":
			b.Expired += count
		case "reconciliation_required":
			b.ReconciliationRequired += count
		}
	}
	return b, rows.Err()
}

func (s *Store) DashboardAttentionScoped(c context.Context, scope string) (int, error) {
	var count int
	queries := []struct{ query, alias string }{
		{`SELECT COUNT(*) FROM billing_purchases p LEFT JOIN account_metadata m ON m.account_id=p.account_id WHERE p.status='reconciliation_required' AND `, "m"},
		{`SELECT COUNT(*) FROM publish_jobs j JOIN repositories r ON r.id=j.repository_id LEFT JOIN account_metadata m ON m.account_id=r.account_id WHERE j.state='failed' AND `, "m"},
		{`SELECT COUNT(*) FROM egress_accounting_uncertain u JOIN repositories r ON r.id=u.repository_id LEFT JOIN account_metadata m ON m.account_id=r.account_id WHERE u.state='uncertain' AND `, "m"},
	}
	for _, item := range queries {
		filter, args := dashboardClassFilter(scope, item.alias)
		var n int
		if err := s.db.QueryRowContext(c, item.query+filter, args...).Scan(&n); err != nil {
			return 0, err
		}
		count += n
	}
	return count, nil
}

func (s *Store) DashboardBilling(c context.Context) (DashboardBilling, error) {
	return s.DashboardBillingScoped(c, "all")
}
func (s *Store) DashboardAttention(c context.Context) (int, error) {
	return s.DashboardAttentionScoped(c, "all")
}
