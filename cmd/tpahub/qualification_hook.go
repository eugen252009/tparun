package main

import (
	"encoding/json"
	"net/http"
	"os"
)

// qualificationCandidateMutation is an opt-in disposable seam for Q-B only.
// It is not registered unless explicitly enabled by the qualification process.
func (s *server) qualificationCandidateReady(w http.ResponseWriter, r *http.Request) {
	configuredToken := os.Getenv("TPAHUB_QUALIFICATION_TOKEN")
	if r.Method != http.MethodGet || os.Getenv("TPAHUB_QUALIFICATION_HOOK") != "1" || configuredToken == "" || r.Header.Get("X-Qualification-Token") != configuredToken {
		http.NotFound(w, r)
		return
	}
	if err := s.db.QualificationCandidateReady(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"db_opened": true})
}

func (s *server) qualificationCandidateMutation(w http.ResponseWriter, r *http.Request) {
	configuredToken := os.Getenv("TPAHUB_QUALIFICATION_TOKEN")
	requestToken := r.Header.Get("X-Qualification-Token")
	if r.Method != http.MethodPost || os.Getenv("TPAHUB_QUALIFICATION_HOOK") != "1" || configuredToken == "" || requestToken == "" || requestToken != configuredToken {
		http.NotFound(w, r)
		return
	}
	if err := s.db.QualificationCandidateMutation(r.Context(), s.currentTime()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"db_opened": true, "mutation_committed": true})
}
