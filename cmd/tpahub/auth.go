package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type ctxKey int

const accountKey ctxKey = 1

func randomToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func accountID(r *http.Request) string { v, _ := r.Context().Value(accountKey).(string); return v }
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

func (s *server) initialTPAGrants(createdAt string) []commerce.Grant {
	if s.cfg.BillingPurchasesEnabled || !s.cfg.LocalProvisioningEnabled {
		return nil
	}
	return defaultTPAGrants(createdAt)
}

func (s *server) accounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || !s.cfg.RegistrationEnabled {
		bad(w, 403, "registration disabled")
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
	if e = s.db.CreateAccount(r.Context(), a, s.initialTPAGrants(a.CreatedAt)...); e != nil {
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
	t, e := randomToken("tph_s_")
	if e != nil {
		return "", e
	}
	return t, s.db.CreateSession(c, store.Session{ID: newID(), AccountID: account, TokenPrefix: t[:10]}, store.HashToken(t))
}
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		bad(w, 405, "method not allowed")
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
	t, e := s.issue(r.Context(), a.ID)
	if e != nil {
		bad(w, 500, "session creation failed")
		return
	}
	jsonOut(w, map[string]any{"account_id": a.ID, "email": a.Email, "token": t})
}
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	t, ok := bearer(r)
	if ok {
		_ = s.db.RevokeSession(r.Context(), store.HashToken(t))
	}
	w.WriteHeader(204)
}
func (s *server) authHandler(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := bearer(r)
		if !ok {
			bad(w, 401, "authentication required")
			return
		}
		id, e := s.db.AccountByToken(r.Context(), store.HashToken(tok))
		if e != nil {
			bad(w, 401, "authentication required")
			return
		}
		fn(w, r.WithContext(context.WithValue(r.Context(), accountKey, id)))
	}
}
