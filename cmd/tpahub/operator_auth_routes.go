package main

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	webauthn "github.com/go-webauthn/webauthn/webauthn"
)

var operatorRecoveryRate = newAuthLimiter(2048, 10, time.Minute)

func (s *server) operatorRecoveryPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.operatorRecover(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	operatorSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = websiteTemplates.ExecuteTemplate(w, "ops-recover.html", nil)
}
func (s *server) operatorRecover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.exactOperatorOrigin(r) {
		bad(w, 403, "invalid request")
		return
	}
	if !operatorRecoveryRate.allow(r.RemoteAddr, s.currentTime()) {
		bad(w, http.StatusTooManyRequests, "too many recovery attempts")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		bad(w, 400, "invalid recovery request")
		return
	}
	if r.URL.RawQuery != "" {
		bad(w, 400, "invalid recovery request")
		return
	}
	code := strings.TrimSpace(r.PostForm.Get("recovery_code"))
	if len(code) < 40 {
		bad(w, 401, "invalid recovery code")
		return
	}
	st, err := s.operatorState()
	if err != nil {
		bad(w, 500, "recovery unavailable")
		return
	}
	sum := sha256Bytes(code)
	now := s.currentTime().UTC().Format(time.RFC3339Nano)
	if err := s.db.ConsumeOperatorRecovery(r.Context(), sum, now); err != nil {
		bad(w, 401, "invalid recovery code")
		return
	}
	token, err := randomOpaque("recovery_")
	if err != nil {
		bad(w, 500, "recovery unavailable")
		return
	}
	st.mu.Lock()
	st.recoverySessions[hashOpaque(token)] = s.currentTime().Add(recoverySessionTTL)
	st.mu.Unlock()
	setOperatorCookie(w, operatorRecoveryCookie, token, "/ops", int(recoverySessionTTL/time.Second))
	http.Redirect(w, r, "/ops/recover/register", 303)
}
func sha256Bytes(value string) []byte { h := sha256.Sum256([]byte(value)); return h[:] }
func (s *server) operatorRegisterPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		bad(w, 405, "method not allowed")
		return
	}
	if !s.recoverySessionValid(r) && !s.requireOperatorSession(r) {
		http.Redirect(w, r, "/ops/login", 303)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	operatorSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = websiteTemplates.ExecuteTemplate(w, "ops-register.html", nil)
}
func (s *server) recoverySessionValid(r *http.Request) bool {
	st, err := s.operatorState()
	if err != nil {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	s.cleanupOperatorState(s.currentTime())
	return s.consumeCookieSession(r, operatorRecoveryCookie, st.recoverySessions)
}
func (s *server) takeRecoveryEnrollment(r *http.Request) (string, bool) {
	c, err := r.Cookie(operatorRecoveryCookie)
	if err != nil {
		return "", false
	}
	st, err := s.operatorState()
	if err != nil {
		return "", false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	key := hashOpaque(c.Value)
	expires, ok := st.recoverySessions[key]
	if !ok || !expires.After(s.currentTime()) {
		delete(st.recoverySessions, key)
		return "", false
	}
	delete(st.recoverySessions, key)
	return "recovery:" + key, true
}
func (s *server) registerBinding(r *http.Request) (string, bool) {
	if c, e := r.Cookie(operatorSessionCookie); e == nil && s.requireOperatorSession(r) {
		return "operator:" + hashOpaque(c.Value), true
	}
	return "", false
}
func (s *server) ceremonyBinding(r *http.Request, kind string) (string, bool) {
	c, err := r.Cookie(operatorCeremonyCookie)
	if err != nil {
		return "", false
	}
	st, err := s.operatorState()
	if err != nil {
		return "", false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	x, ok := st.ceremonies[hashOpaque(c.Value)]
	if !ok || x.Kind != kind || !x.Expires.After(s.currentTime()) {
		return "", false
	}
	return x.Binding, true
}
func (s *server) operatorRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.exactOperatorOrigin(r) {
		bad(w, 403, "invalid request")
		return
	}
	binding, ok := s.takeRecoveryEnrollment(r)
	if !ok {
		binding, ok = s.registerBinding(r)
	}
	if !ok {
		bad(w, 401, "registration unavailable")
		return
	}
	user, err := s.operatorUser(r.Context())
	if err != nil {
		bad(w, 500, "registration unavailable")
		return
	}
	st, err := s.operatorState()
	if err != nil {
		bad(w, 500, "registration unavailable")
		return
	}
	options, session, err := st.webAuthn.BeginRegistration(user, webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired))
	if err != nil {
		bad(w, 500, "registration unavailable")
		return
	}
	token, err := s.beginCeremony("registration", binding, *session)
	if err != nil {
		bad(w, 500, "registration unavailable")
		return
	}
	setOperatorCookie(w, operatorCeremonyCookie, token, "/ops", 120)
	jsonOut(w, options)
}
func (s *server) operatorRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.exactOperatorOrigin(r) {
		bad(w, 403, "invalid request")
		return
	}
	binding, ok := s.ceremonyBinding(r, "registration")
	if !ok {
		bad(w, 401, "registration unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	session, ok := s.takeCeremony(r, "registration", binding)
	if !ok {
		bad(w, 401, "registration failed")
		return
	}
	st, err := s.operatorState()
	if err != nil {
		bad(w, 500, "registration unavailable")
		return
	}
	user, err := s.operatorUser(r.Context())
	if err != nil {
		bad(w, 500, "registration unavailable")
		return
	}
	credential, err := st.webAuthn.FinishRegistration(user, session, r)
	if err != nil {
		bad(w, 401, "registration failed")
		return
	}
	data, err := json.Marshal(credential)
	if err != nil {
		bad(w, 500, "registration unavailable")
		return
	}
	if err = s.db.AddOperatorCredential(r.Context(), credential.ID, data, "", s.currentTime().UTC().Format(time.RFC3339Nano)); err != nil {
		bad(w, 409, "registration failed")
		return
	}
	token, err := s.newOperatorSession()
	if err != nil {
		bad(w, 500, "registration unavailable")
		return
	}
	clearOperatorCookie(w, operatorRecoveryCookie, "/ops")
	clearOperatorCookie(w, operatorCeremonyCookie, "/ops")
	setOperatorCookie(w, operatorSessionCookie, token, "/ops", int(operatorSessionTTL/time.Second))
	jsonOut(w, map[string]bool{"ok": true})
}
func (s *server) operatorLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.exactOperatorOrigin(r) {
		bad(w, 403, "invalid request")
		return
	}
	if c, e := r.Cookie(operatorSessionCookie); e == nil {
		if st, err := s.operatorState(); err == nil {
			st.mu.Lock()
			delete(st.sessions, hashOpaque(c.Value))
			st.mu.Unlock()
		}
	}
	clearOperatorCookie(w, operatorSessionCookie, "/ops")
	w.WriteHeader(http.StatusNoContent)
}
