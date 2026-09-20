package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestQualificationHookRejectsMissingOrInvalidTokens(t *testing.T) {
	oldHook, oldToken := os.Getenv("TPARUN_QUALIFICATION_HOOK"), os.Getenv("TPARUN_QUALIFICATION_TOKEN")
	defer func() {
		_ = os.Setenv("TPARUN_QUALIFICATION_HOOK", oldHook)
		_ = os.Setenv("TPARUN_QUALIFICATION_TOKEN", oldToken)
	}()
	for name, values := range map[string][2]string{
		"disabled":               {"", "secret"},
		"empty configured token": {"1", ""},
		"empty request token":    {"1", "secret"},
		"wrong request token":    {"1", "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			_ = os.Setenv("TPARUN_QUALIFICATION_HOOK", values[0])
			_ = os.Setenv("TPARUN_QUALIFICATION_TOKEN", values[1])
			req := httptest.NewRequest(http.MethodPost, "/__qualification/candidate-mutate", nil)
			if name == "wrong request token" {
				req.Header.Set("X-Qualification-Token", "wrong")
			}
			got := httptest.NewRecorder()
			(&server{}).qualificationCandidateMutation(got, req)
			if got.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404", got.Code)
			}
		})
	}
}
