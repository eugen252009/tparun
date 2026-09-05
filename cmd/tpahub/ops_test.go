package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestOpsResourceValuesRemainStructuredForClientFormatting(t *testing.T) {
	s, _ := testServer(t)
	token, err := s.newOperatorSession()
	if err != nil { t.Fatal(err) }
	r := httptest.NewRequest(http.MethodGet, "/ops?scope=all", nil)
	r.AddCookie(&http.Cookie{Name: operatorSessionCookie, Value: token})
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK { t.Fatalf("dashboard status=%d", w.Code) }
	body := w.Body.String()
	if !strings.Contains(body, `data-used-bytes="0"`) || !strings.Contains(body, `data-limit-bytes="5000000"`) {
		t.Fatalf("dashboard omitted raw resource values: %s", body)
	}
}

func TestOpsRequiresPasskeySession(t *testing.T) {
	s, _ := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/ops", nil)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated dashboard status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/ops/auth/login/begin", nil)
	request.Header.Set("Origin", s.cfg.OperatorOrigin)
	response = httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("login without credential status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/ops", nil)
	request.Header.Set("Authorization", "Bearer legacy-token")
	response = httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("legacy token dashboard status = %d", response.Code)
	}
}

func TestOperatorSessionDoesNotSurviveStateReconstruction(t *testing.T) {
	s, _ := testServer(t); token, err := s.newOperatorSession(); if err != nil { t.Fatal(err) }
	restarted := &server{cfg: s.cfg, db: s.db}; r := httptest.NewRequest(http.MethodGet, "/ops", nil); r.AddCookie(&http.Cookie{Name: operatorSessionCookie, Value: token}); w := httptest.NewRecorder(); restarted.routes().ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther { t.Fatalf("reconstructed session status=%d", w.Code) }
}

func TestRecoveryRejectsQuerySecrets(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.OperatorOrigin = "https://tpa.lupricht.net"
	r := httptest.NewRequest(http.MethodPost, "/ops/recover?recovery_code=TPA-RCV-invalid", nil)
	r.Header.Set("Origin", s.cfg.OperatorOrigin)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("query recovery status=%d", w.Code)
	}
}

func TestRecoveryEnrollmentSessionIsSingleUseConcurrently(t *testing.T) {
	s, _ := testServer(t)
	st, err := s.operatorState()
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.recoverySessions[hashOpaque("recovery-test")] = s.currentTime().Add(time.Minute)
	st.mu.Unlock()
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, "/ops/auth/register/begin", nil)
			r.AddCookie(&http.Cookie{Name: operatorRecoveryCookie, Value: "recovery-test"})
			_, ok := s.takeRecoveryEnrollment(r)
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	count := 0
	for ok := range results {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("recovery enrollment grants=%d", count)
	}
}

func TestOperatorWebAuthnInitFailureIsNotCached(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.OperatorRelyingPartyID = ":invalid"
	if _, err := s.operatorState(); err == nil {
		t.Fatal("invalid RP accepted")
	}
	if st, err := s.operatorState(); err == nil || st != nil {
		t.Fatalf("failed verifier cached: state=%v err=%v", st, err)
	}
}

func TestAccountClassMigrationAndValidation(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.CreateAccount(ctx, store.Account{ID: "class-account", Email: "class@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	a, err := db.AccountByID(ctx, "class-account")
	if err != nil || a.AccountClass != store.AccountClassUnclassified {
		t.Fatalf("default class = %q, err=%v", a.AccountClass, err)
	}
	if err := db.SetAccountClass(ctx, "class-account", store.AccountClassTest); err != nil {
		t.Fatal(err)
	}
	a, err = db.AccountByID(ctx, "class-account")
	if err != nil || a.AccountClass != store.AccountClassTest {
		t.Fatalf("updated class = %q, err=%v", a.AccountClass, err)
	}
	if err := db.SetAccountClass(ctx, "class-account", "billing"); err == nil {
		t.Fatal("invalid class accepted")
	}
}
