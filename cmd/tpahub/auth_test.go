package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthenticationAndPrivateReader(t *testing.T) {
	s, h := testServer(t)
	reg := func(email string) string {
		req, _ := http.NewRequest("POST", h.URL+"/v1/accounts", strings.NewReader(`{"email":"`+email+`","password":"long-enough-password"}`))
		req.Header.Set("Content-Type", "application/json")
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		var x struct {
			Token string `json:"token"`
		}
		json.NewDecoder(res.Body).Decode(&x)
		if res.StatusCode != 200 {
			t.Fatalf("register %d", res.StatusCode)
		}
		return x.Token
	}
	tok := reg("new@example.invalid")
	req, _ := http.NewRequest("POST", h.URL+"/v1/repositories", strings.NewReader(`{"name":"private"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, _ := http.DefaultClient.Do(req)
	var repo struct {
		ID string `json:"id"`
	}
	json.NewDecoder(res.Body).Decode(&repo)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	req, _ = http.NewRequest("POST", h.URL+"/v1/repositories/"+repo.ID+"/reader-credentials", strings.NewReader(`{"name":"client"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, _ = http.DefaultClient.Do(req)
	var cr struct {
		Token string `json:"token"`
	}
	json.NewDecoder(res.Body).Decode(&cr)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	p := filepath.Join(s.cfg.DataDir, "repos", repo.ID, "x")
	os.WriteFile(p, []byte("ok"), 0644)
	if res, _ = http.Get(h.URL + "/r/" + repo.ID + "/x"); res.StatusCode != 401 {
		t.Fatalf("anonymous status %d", res.StatusCode)
	}
	req, _ = http.NewRequest("GET", h.URL+"/r/"+repo.ID+"/x", nil)
	req.SetBasicAuth("reader", cr.Token)
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 200 {
		t.Fatalf("reader status %d", res.StatusCode)
	}
}
