//go:build qualificationhooks

package main

import "net/http"

func registerOptionalRoutes(mux *http.ServeMux, s *server) {
	if tparunEnv("TPARUN_QUALIFICATION_HOOK") != "1" {
		return
	}
	mux.HandleFunc("/__qualification/candidate-ready", s.qualificationCandidateReady)
	mux.HandleFunc("/__qualification/candidate-mutate", s.qualificationCandidateMutation)
}
