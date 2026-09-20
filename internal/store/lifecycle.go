package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

type AccountLifecycleState string

const (
	AccountLifecycleActive            AccountLifecycleState = "active"
	AccountLifecycleGrace             AccountLifecycleState = "grace"
	AccountLifecycleExtendedGrace     AccountLifecycleState = "extended_grace"
	AccountLifecycleMembershipExpired AccountLifecycleState = "membership_expired" // ephemeral hard expiry
	AccountLifecyclePurgeDue          AccountLifecycleState = "purge_due"
)

const (
	CustomerGracePeriod     = 30 * 24 * time.Hour
	CustomerRetentionPeriod = 60 * 24 * time.Hour
)

// AccountTTLProfile is a named server-owned creation profile, not a duration.
type AccountTTLProfile string

const AccountTTLMin AccountTTLProfile = "min"

func ParseAccountTTLProfile(value string) (AccountTTLProfile, bool) {
	if value == "" {
		return "", true
	}
	if value == string(AccountTTLMin) {
		return AccountTTLMin, true
	}
	return "", false
}

func MinEphemeralDeadlines(created time.Time) (membershipExpiresAt, purgeAfter string) {
	created = created.UTC()
	return created.Add(tpahubproduct.MinEphemeralLifetime).Format(time.RFC3339Nano), created.Add(tpahubproduct.MinEphemeralPurgeDelay).Format(time.RFC3339Nano)
}

// EvaluateAccountLifecycle derives lifecycle state from persisted timestamps.
// It is intentionally policy-only: it does not delete or mutate account state.
func EvaluateAccountLifecycle(account Account, now time.Time) (AccountLifecycleState, error) {
	if !account.LifecycleKind.Valid() {
		return "", fmt.Errorf("invalid account lifecycle kind %q", account.LifecycleKind)
	}
	if account.LifecycleKind == AccountLifecycleInternal {
		return AccountLifecycleActive, nil
	}
	expires, err := parseLifecycleTime(account.MembershipExpiresAt)
	if err != nil {
		return "", fmt.Errorf("membership expiry: %w", err)
	}
	purge, err := parseLifecycleTime(account.PurgeAfter)
	if err != nil {
		return "", fmt.Errorf("purge deadline: %w", err)
	}
	if purge != nil && expires != nil && purge.Before(*expires) {
		return "", fmt.Errorf("purge deadline precedes membership expiry")
	}
	if purge != nil && !now.Before(*purge) {
		return AccountLifecyclePurgeDue, nil
	}
	if expires == nil || now.Before(*expires) {
		return AccountLifecycleActive, nil
	}
	if account.LifecycleKind == AccountLifecycleEphemeral {
		return AccountLifecycleMembershipExpired, nil
	}
	if now.Before(expires.Add(CustomerGracePeriod)) {
		return AccountLifecycleGrace, nil
	}
	return AccountLifecycleExtendedGrace, nil
}

func parseLifecycleTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func AddCustomerRetention(expiry time.Time) time.Time {
	return expiry.UTC().Add(CustomerRetentionPeriod)
}

// SetMembershipExpiry atomically updates the authoritative membership and
// retention deadlines. Phase 2 does not perform cleanup.
func (s *Store) SetMembershipExpiry(ctx context.Context, accountID, expiry string) error {
	parsed, err := parseLifecycleTime(expiry)
	if err != nil {
		return fmt.Errorf("invalid membership expiry: %w", err)
	}
	var kind AccountLifecycleKind
	if err = s.db.QueryRowContext(ctx, `SELECT lifecycle_kind FROM accounts WHERE id=?`, accountID).Scan(&kind); err != nil {
		return err
	}
	if kind == AccountLifecycleInternal {
		return fmt.Errorf("internal accounts do not have membership expiry")
	}
	purge := any(nil)
	if parsed != nil {
		if kind == AccountLifecycleCustomer {
			purge = AddCustomerRetention(*parsed).Format(time.RFC3339Nano)
		} else {
			purge = parsed.Format(time.RFC3339Nano)
		}
	}
	result, err := s.db.ExecContext(ctx, `UPDATE accounts SET membership_expires_at=?,purge_after=? WHERE id=?`, nullableLifecycleTime(expiry), purge, accountID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
