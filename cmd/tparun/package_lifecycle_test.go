package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPackageLifecycleHTTPRequiresDeleteConfirmationAndIdempotency(t *testing.T) {
	server, handler := testServer(t)
	repositoryID := createLocalPolicyRepo(t, server, "tph_s_test", "public")
	post := func(operation string, input packageLifecycleRequest, key string) *http.Response {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPost, handler.URL+"/v1/repositories/"+repositoryID+"/packages/"+operation, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer tph_s_test")
		request.Header.Set("Content-Type", "application/json")
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response, err := handler.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := post("delete", packageLifecycleRequest{Package: "example", Version: "1.0", Architecture: "amd64"}, strings.Repeat("a", 32))
	response.Body.Close()
	if response.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("unconfirmed delete status=%d", response.StatusCode)
	}
	response = post("delete", packageLifecycleRequest{Package: "example", Version: "1.0", Architecture: "amd64", Confirmed: true}, "")
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing idempotency key status=%d", response.StatusCode)
	}
	response = post("unlist", packageLifecycleRequest{Package: "../example", Version: "1.0", Architecture: "amd64"}, strings.Repeat("b", 32))
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsafe identity status=%d", response.StatusCode)
	}
	for _, key := range []string{strings.Repeat("a", 32), strings.Repeat("b", 32)} {
		if _, err := server.db.PackageLifecycleByIdempotency(context.Background(), repositoryID, "a", key); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("rejected request created lifecycle job for key %s: %v", key, err)
		}
	}
}

func TestPackageLifecycleCLIConfirmationAndAPIContract(t *testing.T) {
	const repositoryID = "0123456789abcdef0123456789abcdef"
	requests := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/repositories/"+repositoryID+"/packages/delete" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer fixture-token" || !idRE.MatchString(r.Header.Get("Idempotency-Key")) {
			t.Errorf("authorization/idempotency headers missing")
		}
		var input packageLifecycleRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || !input.Confirmed || input.Package != "example" || input.Version != "1.0" || input.Architecture != "amd64" {
			t.Errorf("delete request=%+v err=%v", input, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(packageLifecycleResponse{ID: strings.Repeat("c", 32), State: "succeeded", GenerationID: strings.Repeat("d", 32), Operation: "delete", Package: "example", Version: "1.0", Architecture: "amd64", MetadataState: "published", ArtifactState: "deleted"})
	}))
	defer api.Close()
	oldInteractive, oldPrompt := publishInteractive, publishPrompt
	publishInteractive = func() bool { return true }
	publishPrompt = func(string) string { return "n" }
	defer func() { publishInteractive, publishPrompt = oldInteractive, oldPrompt }()
	client := cliConfig{Server: api.URL, state: &clientState{Credentials: map[string]clientCredential{api.URL: {Token: "fixture-token"}}}}
	if err := packageLifecycleCLI(client, []string{"delete", api.URL + "/" + repositoryID, "example", "1.0", "amd64"}); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatal("cancelled CLI deletion contacted the API")
	}
	if err := packageLifecycleCLI(client, []string{"delete", api.URL + "/" + repositoryID, "example", "1.0", "amd64", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("API requests=%d", requests)
	}
	if err := packageLifecycleCLI(client, []string{"unlist", api.URL + "/" + repositoryID, "example", "1.0", "amd64", "--yes"}); err == nil {
		t.Fatal("unlist accepted the delete-only confirmation flag")
	}
}
