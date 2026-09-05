package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	webauthn "github.com/go-webauthn/webauthn/webauthn"
)

const (
	operatorSessionCookie  = "tpahub_operator_session"
	operatorRecoveryCookie = "tpahub_operator_recovery"
	operatorCeremonyCookie = "tpahub_operator_ceremony"
	operatorSessionTTL     = 8 * time.Hour
	recoverySessionTTL     = 10 * time.Minute
	ceremonyTTL            = 2 * time.Minute
	recoveryCodeTTL        = 15 * time.Minute
)

type operatorUser struct {
	ID          []byte
	Credentials []webauthn.Credential
}

func (u operatorUser) WebAuthnID() []byte                         { return u.ID }
func (u operatorUser) WebAuthnName() string                       { return "TPA Hub Operator" }
func (u operatorUser) WebAuthnDisplayName() string                { return "TPA Hub Operator" }
func (u operatorUser) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

type operatorCeremony struct {
	Kind    string
	Session webauthn.SessionData
	Binding string
	Expires time.Time
}
type operatorAuthState struct {
	mu               sync.Mutex
	webAuthn         *webauthn.WebAuthn
	sessions         map[string]time.Time
	recoverySessions map[string]time.Time
	ceremonies       map[string]operatorCeremony
}

func (s *server) operatorState() (*operatorAuthState, error) {
	s.operatorInitMu.Lock()
	defer s.operatorInitMu.Unlock()
	if s.operatorAuth != nil {
		return s.operatorAuth, nil
	}
	s.operatorAuth = &operatorAuthState{sessions: make(map[string]time.Time), recoverySessions: make(map[string]time.Time), ceremonies: make(map[string]operatorCeremony)}
	cfg := &webauthn.Config{RPDisplayName: "TPA Hub", RPID: s.cfg.OperatorRelyingPartyID, RPOrigins: []string{s.cfg.OperatorOrigin}, AttestationPreference: protocol.PreferNoAttestation, AuthenticatorSelection: protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: protocol.ResidentKeyRequired(), UserVerification: protocol.VerificationRequired}, Timeouts: webauthn.TimeoutsConfig{Login: webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTTL}, Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTTL}}}
	w, err := webauthn.New(cfg)
	if err != nil {
		return nil, err
	}
	s.operatorAuth.webAuthn = w
	return s.operatorAuth, nil
}

func randomOpaque(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func hashOpaque(v string) string {
	h := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func (s *server) operatorUser(ctx context.Context) (operatorUser, error) {
	handle, err := s.db.OperatorUserHandle(ctx)
	if err != nil {
		return operatorUser{}, err
	}
	records, err := s.db.OperatorCredentials(ctx)
	if err != nil {
		return operatorUser{}, err
	}
	out := operatorUser{ID: handle, Credentials: make([]webauthn.Credential, 0, len(records))}
	for _, record := range records {
		var credential webauthn.Credential
		if err := json.Unmarshal(record.Data, &credential); err != nil {
			return operatorUser{}, fmt.Errorf("invalid stored operator credential: %w", err)
		}
		out.Credentials = append(out.Credentials, credential)
	}
	return out, nil
}
func (s *server) newOperatorSession() (string, error) {
	token, err := randomOpaque("ops_")
	if err != nil {
		return "", err
	}
	st, _ := s.operatorState()
	st.mu.Lock()
	defer st.mu.Unlock()
	s.cleanupOperatorState(s.currentTime())
	st.sessions[hashOpaque(token)] = s.currentTime().Add(operatorSessionTTL)
	return token, nil
}
func (s *server) requireOperatorSession(r *http.Request) bool {
	c, err := r.Cookie(operatorSessionCookie)
	if err != nil {
		return false
	}
	st, err := s.operatorState()
	if err != nil {
		return false
	}
	key := hashOpaque(c.Value)
	now := s.currentTime()
	st.mu.Lock()
	defer st.mu.Unlock()
	expires, ok := st.sessions[key]
	if !ok || !expires.After(now) {
		delete(st.sessions, key)
		return false
	}
	return true
}
func (s *server) consumeCookieSession(r *http.Request, name string, sessions map[string]time.Time) bool {
	c, err := r.Cookie(name)
	if err != nil {
		return false
	}
	key := hashOpaque(c.Value)
	now := s.currentTime()
	expires, ok := sessions[key]
	if !ok || !expires.After(now) {
		delete(sessions, key)
		return false
	}
	return true
}
func setOperatorCookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}
func clearOperatorCookie(w http.ResponseWriter, name, path string) {
	setOperatorCookie(w, name, "", path, -1)
}
func (s *server) exactOperatorOrigin(r *http.Request) bool {
	return r.Header.Get("Origin") == s.cfg.OperatorOrigin
}
func (s *server) cleanupOperatorState(now time.Time) {
	st := s.operatorAuth
	if st == nil {
		return
	}
	for k, v := range st.sessions {
		if !v.After(now) {
			delete(st.sessions, k)
		}
	}
	for k, v := range st.recoverySessions {
		if !v.After(now) {
			delete(st.recoverySessions, k)
		}
	}
	for k, v := range st.ceremonies {
		if !v.Expires.After(now) {
			delete(st.ceremonies, k)
		}
	}
}
func (s *server) beginCeremony(kind, binding string, session webauthn.SessionData) (string, error) {
	token, err := randomOpaque("cer_")
	if err != nil {
		return "", err
	}
	st, e := s.operatorState()
	if e != nil {
		return "", e
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	s.cleanupOperatorState(s.currentTime())
	st.ceremonies[hashOpaque(token)] = operatorCeremony{Kind: kind, Binding: binding, Session: session, Expires: s.currentTime().Add(ceremonyTTL)}
	return token, nil
}
func (s *server) takeCeremony(r *http.Request, kind, binding string) (webauthn.SessionData, bool) {
	c, e := r.Cookie(operatorCeremonyCookie)
	if e != nil {
		return webauthn.SessionData{}, false
	}
	st, e := s.operatorState()
	if e != nil {
		return webauthn.SessionData{}, false
	}
	key := hashOpaque(c.Value)
	st.mu.Lock()
	defer st.mu.Unlock()
	x, ok := st.ceremonies[key]
	delete(st.ceremonies, key)
	if !ok || x.Kind != kind || x.Binding != binding || !x.Expires.After(s.currentTime()) {
		return webauthn.SessionData{}, false
	}
	return x.Session, true
}

func (s *server) operatorLoginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		bad(w, 405, "method not allowed")
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = websiteTemplates.ExecuteTemplate(w, "ops-login.html", nil)
}
func (s *server) operatorLoginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.exactOperatorOrigin(r) {
		bad(w, 403, "invalid request")
		return
	}
	if !webRateAllowed(r.RemoteAddr) {
		bad(w, http.StatusTooManyRequests, "too many authentication attempts")
		return
	}
	st, err := s.operatorState()
	if err != nil {
		bad(w, 500, "operator authentication unavailable")
		return
	}
	options, session, err := st.webAuthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		bad(w, 401, "operator authentication unavailable")
		return
	}
	token, err := s.beginCeremony("login", "", *session)
	if err != nil {
		bad(w, 500, "operator authentication unavailable")
		return
	}
	setOperatorCookie(w, operatorCeremonyCookie, token, "/ops", 120)
	jsonOut(w, options)
}
func (s *server) operatorLoginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.exactOperatorOrigin(r) {
		bad(w, 403, "invalid request")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	session, ok := s.takeCeremony(r, "login", "")
	if !ok {
		bad(w, 401, "operator authentication failed")
		return
	}
	st, err := s.operatorState()
	if err != nil {
		bad(w, 401, "operator authentication failed")
		return
	}
	credential, err := st.webAuthn.FinishDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		u, e := s.operatorUser(r.Context())
		if e != nil {
			return nil, e
		}
		if !bytesEqual(u.ID, userHandle) {
			return nil, fmt.Errorf("unknown operator")
		}
		for _, c := range u.Credentials {
			if bytesEqual(c.ID, rawID) {
				return u, nil
			}
		}
		return nil, fmt.Errorf("unknown operator")
	}, session, r)
	if err != nil || credential == nil {
		bad(w, 401, "operator authentication failed")
		return
	}
	data, _ := json.Marshal(credential)
	if err = s.db.TouchOperatorCredential(r.Context(), credential.ID, data, s.currentTime().UTC().Format(time.RFC3339Nano)); err != nil {
		bad(w, 500, "operator authentication unavailable")
		return
	}
	token, err := s.newOperatorSession()
	if err != nil {
		bad(w, 500, "operator authentication unavailable")
		return
	}
	clearOperatorCookie(w, operatorCeremonyCookie, "/ops")
	setOperatorCookie(w, operatorSessionCookie, token, "/ops", int(operatorSessionTTL/time.Second))
	jsonOut(w, map[string]bool{"ok": true})
}
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var x byte
	for i := range a {
		x |= a[i] ^ b[i]
	}
	return x == 0
}
