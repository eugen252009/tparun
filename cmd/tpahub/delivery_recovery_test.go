package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/usage"
)

func TestLocalDeliveryObservationDoesNotHideStrictUncertainty(t *testing.T) {
	consumer := usage.NewMailbox(4)
	s, _, _ := deliveryFixture(t, config.DeliveryStrictAccounted, consumer)
	s.completeEgressDelivery = func(context.Context, string, string, string, int64, string) error {
		return errors.New("injected settlement failure")
	}
	w := deliveryRequest(s, "GET", "package.deb", nil)
	if w.Code != 200 || w.Body.Len() != 10 {
		t.Fatalf("delivery=%d %q", w.Code, w.Body.String())
	}
	fact := <-consumer.Events()
	if fact.Bytes != 10 {
		t.Fatalf("observation=%+v", fact)
	}
	markers, err := s.db.EgressDeliveries(context.Background(), "a")
	if err != nil || len(markers) != 1 || markers[0].State != "uncertain" {
		t.Fatalf("uncertainty=%+v %v", markers, err)
	}
	n, err := s.db.RepositoryEgress(context.Background(), deliveryTestRepo, "2026-08")
	if err != nil || n != 0 {
		t.Fatalf("observation incorrectly settled ledger=%d %v", n, err)
	}
	if w := deliveryRequest(s, "GET", "package.deb", nil); w.Code != 503 {
		t.Fatalf("uncertain delivery=%d", w.Code)
	}
	if len(consumer.Events()) != 0 {
		t.Fatal("rejected request emitted observation")
	}
	// Reconstruct runtime state: durable uncertainty alone must still reject.
	restarted := &server{cfg: s.cfg, db: s.db, now: s.now}
	w = httptest.NewRecorder()
	restarted.routes().ServeHTTP(w, httptest.NewRequest("GET", "/r/"+deliveryTestRepo+"/package.deb", nil))
	if w.Code != 503 {
		t.Fatalf("reconstructed strict authority=%d", w.Code)
	}
}

func TestLocalDeliveryHTTPModesEquivalent(t *testing.T) {
	type response struct {
		status  int
		body    string
		headers map[string]string
	}
	var expected []response
	for _, mode := range localDeliveryModes {
		t.Run(mode, func(t *testing.T) {
			s, root, _ := deliveryFixture(t, mode)
			for _, name := range []string{"package.deb", "index.html"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("0123456789"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(filepath.Join(root, name), deliveryTestTime, deliveryTestTime); err != nil {
					t.Fatal(err)
				}
			}
			cases := []struct {
				method, path string
				headers      map[string]string
			}{
				{"GET", "package.deb", nil},
				{"GET", "package.deb", map[string]string{"Range": "bytes=2-5"}},
				{"GET", "package.deb", map[string]string{"Range": "bytes=0-1,4-5"}},
				{"HEAD", "package.deb", nil},
				{"GET", "package.deb", map[string]string{"If-Modified-Since": deliveryTestTime.Format(http.TimeFormat)}},
				{"GET", "package.deb", map[string]string{"If-Unmodified-Since": deliveryTestTime.Add(-time.Hour).Format(http.TimeFormat)}},
				{"GET", "package.deb", map[string]string{"Range": "bytes=999-"}},
				{"GET", "index.html", nil},
			}
			var got []response
			var bytes int64
			for _, tc := range cases {
				w := deliveryRequest(s, tc.method, tc.path, tc.headers)
				h := map[string]string{}
				for _, k := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "Location"} {
					h[k] = w.Header().Get(k)
				}
				body := w.Body.String()
				bytes += int64(w.Body.Len())
				// Multipart ranges have randomized MIME boundaries. Assert payload pieces
				// and account the actual body rather than comparing those random bytes.
				if tc.headers["Range"] == "bytes=0-1,4-5" {
					if w.Code != 206 || !(strings.Contains(body, "\r\n\r\n01\r\n") && strings.Contains(body, "\r\n\r\n45\r\n")) {
						t.Fatalf("multipart range=%d %q", w.Code, body)
					}
					body = "multipart with both requested parts"
				}
				got = append(got, response{w.Code, body, h})
			}
			if mode != config.DeliveryUnmetered {
				view, err := s.readDeliveryUsage("a", deliveryTestTime)
				if err != nil || view.Bytes != bytes {
					t.Fatalf("actual HTTP body bytes=%d view=%+v err=%v", bytes, view, err)
				}
			}
			if expected == nil {
				expected = got
			} else if !reflect.DeepEqual(got, expected) {
				t.Fatalf("HTTP mechanics differ: got=%+v expected=%+v", got, expected)
			}
		})
	}
}
