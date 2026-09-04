package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
)

func (s *server) credentials(w http.ResponseWriter, r *http.Request, repo string, tail []string) {
	if accountID(r) == "" {
		bad(w, 401, "authentication required")
		return
	}
	owned, e := s.db.OwnedRepository(r.Context(), accountID(r), repo)
	if e != nil {
		bad(w, 404, "repository not found")
		return
	}
	if _, e = s.grantService().GetGrant(r.Context(), commerce.CustomerID(accountID(r)), commerce.ProductTPAHub, CapabilityRepositoryReadPrivate, s.currentTime()); e != nil {
		bad(w, http.StatusForbidden, "private repository grant unavailable")
		return
	}
	if len(tail) == 0 && r.Method == "GET" {
		xs, e := s.db.Credentials(r.Context(), accountID(r), owned.ID)
		if e != nil {
			bad(w, 500, "credential lookup failed")
			return
		}
		jsonOut(w, xs)
		return
	}
	if len(tail) == 0 && r.Method == "POST" {
		var in struct {
			Name string `json:"name"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil || in.Name == "" {
			bad(w, 400, "credential name required")
			return
		}
		tok, e := randomToken("tph_r_")
		if e != nil {
			bad(w, 500, "token generation failed")
			return
		}
		x := store.ReaderCredential{ID: newID(), RepositoryID: owned.ID, Name: in.Name, TokenPrefix: tok[:10], CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if e = s.db.CreateCredential(r.Context(), x, store.HashToken(tok)); e != nil {
			bad(w, 409, "credential already exists")
			return
		}
		jsonOut(w, map[string]any{"id": x.ID, "name": x.Name, "token": tok})
		return
	}
	if len(tail) == 1 && r.Method == "DELETE" {
		if e = s.db.RevokeCredential(r.Context(), accountID(r), owned.ID, tail[0]); e != nil {
			bad(w, 500, fmt.Sprint(e))
			return
		}
		w.WriteHeader(204)
		return
	}
	bad(w, 404, "not found")
}
