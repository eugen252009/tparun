package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type ctxKey int

const (
	accountKey         ctxKey = 1
	credentialFlagsKey ctxKey = 2
)

func randomToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func accountID(r *http.Request) string { v, _ := r.Context().Value(accountKey).(string); return v }
func credentialFlags(r *http.Request) (store.CredentialFlags, bool) {
	v, ok := r.Context().Value(credentialFlagsKey).(store.CredentialFlags)
	return v, ok
}
func allowsCredential(r *http.Request, flag store.CredentialFlags) bool {
	f, ok := credentialFlags(r)
	return !ok || f.Has(flag)
}
func bearer(r *http.Request) (string, bool) {
	p := strings.Fields(r.Header.Get("Authorization"))
	return func() (string, bool) {
		if len(p) == 2 && strings.EqualFold(p[0], "bearer") {
			return p[1], true
		}
		return "", false
	}()
}

var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func (s *server) accounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || !s.cfg.RegistrationEnabled {
		bad(w, 403, "registration disabled")
		return
	}
	if !s.customerAuthAllowed(r) {
		bad(w, http.StatusTooManyRequests, "too many authentication attempts")
		return
	}
	var in struct{ Email, Password string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in) != nil || !emailRE.MatchString(strings.TrimSpace(strings.ToLower(in.Email))) || len(in.Password) < 10 {
		bad(w, 400, "valid email and password of at least 10 characters required")
		return
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if e != nil {
		bad(w, 500, "password hashing failed")
		return
	}
	a := store.Account{ID: newID(), Email: strings.ToLower(strings.TrimSpace(in.Email)), PasswordHash: string(hash), Status: store.AccountStatusActive, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	grants, e := s.initialTPAGrants(a.CreatedAt)
	if e != nil {
		bad(w, 500, "account provisioning unavailable")
		return
	}
	if e = s.db.CreateAccount(r.Context(), a, grants...); e != nil {
		bad(w, 409, "account already exists")
		return
	}
	out, e := s.issue(r.Context(), a.ID)
	if e != nil {
		bad(w, 500, "session creation failed")
		return
	}
	jsonOut(w, map[string]any{"account_id": a.ID, "email": a.Email, "token": out})
}
func (s *server) issue(c context.Context, account string) (string, error) {
	return s.issueWithCredential(c, account, "")
}
func (s *server) issueWithCredential(c context.Context, account, credential string) (string, error) {
	t, e := randomToken("tph_s_")
	if e != nil {
		return "", e
	}
	return t, s.db.CreateSession(c, store.Session{ID: newID(), AccountID: account, TokenPrefix: t[:10], CredentialID: credential}, store.HashToken(t))
}
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		bad(w, 405, "method not allowed")
		return
	}
	if !s.customerAuthAllowed(r) {
		bad(w, http.StatusTooManyRequests, "too many authentication attempts")
		return
	}
	var in struct{ Email, Password string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in) != nil {
		bad(w, 401, "invalid credentials")
		return
	}
	a, e := s.db.ActiveAccountByEmail(r.Context(), strings.ToLower(strings.TrimSpace(in.Email)))
	if e != nil || bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(in.Password)) != nil {
		time.Sleep(150 * time.Millisecond)
		bad(w, 401, "invalid credentials")
		return
	}
	if !s.lifecycleAllowsAuthentication(a) {
		bad(w, 401, "invalid credentials")
		return
	}
	t, e := s.issue(r.Context(), a.ID)
	if e != nil {
		bad(w, 500, "session creation failed")
		return
	}
	jsonOut(w, map[string]any{"account_id": a.ID, "email": a.Email, "token": t})
}
func (s *server) customerAuthAllowed(r *http.Request) bool {
	if s.customerAuthRate == nil {
		s.customerAuthRate = newAuthLimiter(4096, 10, time.Minute)
	}
	return s.customerAuthRate.allow(r.RemoteAddr, s.currentTime())
}
func normalAccountSession(r *http.Request) bool { _, ssh := credentialFlags(r); return !ssh }

func (s *server) accountSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := accountID(r)
	a, err := s.db.AccountByID(r.Context(), id)
	if err != nil {
		bad(w, http.StatusNotFound, "account not found")
		return
	}
	usage, err := s.accountUsage(r.Context(), id)
	if err != nil {
		bad(w, http.StatusInternalServerError, "account usage unavailable")
		return
	}
	lifecycleState, lifecycleErr := store.EvaluateAccountLifecycle(a, s.currentTime())
	if lifecycleErr != nil {
		bad(w, http.StatusInternalServerError, "account lifecycle unavailable")
		return
	}
	result := map[string]any{
		"id":                    a.ID,
		"email":                 a.Email,
		"account_class":         a.AccountClass,
		"lifecycle_kind":        a.LifecycleKind,
		"membership_expires_at": a.MembershipExpiresAt,
		"purge_after":           a.PurgeAfter,
		"lifecycle_state":       lifecycleState,
		"repository_count":      usage.RepositoryCount,
		"package_count":         usage.PackageCount,
		"storage_bytes":         usage.StoredBytes,
		"storage_limit_state":   "none",
	}
	if grant, grantErr := s.grantService().GetGrant(r.Context(), commerce.CustomerID(id), tpahubproduct.ProductID, CapabilityRepositoryPublish, s.currentTime()); grantErr == nil {
		result["storage_limit_state"] = "finite"
		result["storage_limit_bytes"] = grant.Limits.StorageBytes
	}
	jsonOut(w, result)
}
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	t, ok := bearer(r)
	if ok {
		_ = s.db.RevokeSession(r.Context(), store.HashToken(t))
	}
	w.WriteHeader(204)
}
func (s *server) lifecycleAllowsAuthentication(a store.Account) bool {
	state, err := store.EvaluateAccountLifecycle(a, s.currentTime())
	return err == nil && (a.LifecycleKind != store.AccountLifecycleEphemeral || state == store.AccountLifecycleActive)
}

func (s *server) authHandler(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := bearer(r)
		if !ok {
			bad(w, 401, "authentication required")
			return
		}
		principal, e := s.db.SessionPrincipalByToken(r.Context(), store.HashToken(tok))
		if e != nil {
			bad(w, 401, "authentication required")
			return
		}
		ctx := context.WithValue(r.Context(), accountKey, principal.AccountID)
		if principal.CredentialID != "" {
			ctx = context.WithValue(ctx, credentialFlagsKey, principal.CredentialFlags)
		}
		fn(w, r.WithContext(ctx))
	}
}
