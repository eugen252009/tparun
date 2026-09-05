package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestOpsRequiresOperatorToken(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.OperatorToken = "operator-secret"
	request := httptest.NewRequest(http.MethodGet, "/ops", nil)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unauthenticated dashboard status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/ops?scope=all", nil)
	request.Header.Set("Authorization", "Bearer operator-secret")
	response = httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated dashboard status = %d", response.Code)
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
