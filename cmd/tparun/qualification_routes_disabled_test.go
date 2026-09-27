//go:build !qualificationhooks

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQualificationRoutesAreNotRegisteredInReleaseBuild(t *testing.T) {
	t.Setenv("TPARUN_QUALIFICATION_HOOK", "1")
	t.Setenv("TPARUN_QUALIFICATION_TOKEN", "qualification-test-token")
	mux := http.NewServeMux()
	registerOptionalRoutes(mux, &server{})
	for _, path := range []string{
		"/__qualification/candidate-ready",
		"/__qualification/candidate-mutate",
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("qualification route %q status=%d, want 404", path, recorder.Code)
		}
	}
}
