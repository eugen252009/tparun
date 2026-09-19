package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eugen252009/tpahub/internal/config"
)

func TestClientCommandsRejectCrossOriginReplay(t *testing.T) {
	clientTestHome(t)
	var arrivals atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { arrivals.Add(1); io.Copy(io.Discard, r.Body) }))
	defer target.Close()
	for _, code := range []int{307, 308} {
		for _, args := range [][]string{
			{"login"}, {"register"}, {"logout"}, {"status", "job"},
		} {
			t.Run(fmt.Sprint(code, args[:1]), func(t *testing.T) {
				var sensitive atomic.Bool
				src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if r.Header.Get("Authorization") == "Bearer scoped-fixture" || strings.Contains(string(body), "origin-test-password") || strings.Contains(string(body), "private-publication-fixture") {
						sensitive.Store(true)
					}
					w.Header().Set("Location", target.URL)
					w.WriteHeader(code)
				}))
				defer src.Close()
				c := loadCLI().selectServer(src.URL)
				c.Token = "scoped-fixture"
				if err := saveCLI(c); err != nil {
					t.Fatal(err)
				}
				argv := append(append([]string{}, args...), "--server", src.URL)
				if err := cli(config.Config{}, argv); err == nil {
					t.Fatal("cross-origin command unexpectedly succeeded")
				}
				if arrivals.Load() != 0 {
					t.Fatal("foreign target received request")
				}
				if !sensitive.Load() {
					t.Fatal("test did not exercise a sensitive production request")
				}
			})
		}
	}
}
