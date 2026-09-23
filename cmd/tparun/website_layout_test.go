package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Exercise the actual router and rendered templates, including authenticated
// account/operator pages, rather than a second implementation of the layout.
func TestWebsiteSharedShellAndAccessBoundaries(t *testing.T) {
	s, _ := testServer(t)
	operator, err := s.newOperatorSession()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, cookie, token string
		status              int
	}{
		{"/", "", "", 200},
		{"/docs", "", "", 200},
		{"/how-to", "", "", 200},
		{"/pricing", "", "", 200},
		{"/nano", "", "", 200},
		{"/login", "", "", 200},
		{"/register", "", "", 200},
		{"/account", "tpahub_session", "tph_s_test", 200},
		{"/ops", operatorSessionCookie, operator, 200},
		{"/ops/bootstrap", operatorSessionCookie, operator, 200},
		{"/ops/passkeys/add", operatorSessionCookie, operator, 200},
		{"/ops/login", "", "", 200},
		{"/ops/recover", "", "", 200},
		{"/missing-page", "", "", 404},
		{"/account", "", "", 303},
		{"/ops", "", "", 303},
		{"/ops/bootstrap", "", "", 303},
		{"/ops/passkeys/add", "", "", 303},
	} {
		t.Run(tc.path+"/"+tc.cookie, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: tc.cookie, Value: tc.token})
			}
			w := httptest.NewRecorder()
			s.routes().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d, want %d", w.Code, tc.status)
			}
			if tc.status == 303 {
				if strings.Contains(w.Body.String(), "Your workspace") || strings.Contains(w.Body.String(), "data-logout-form") {
					t.Fatal("unauthenticated response exposed protected page")
				}
				return
			}
			body := w.Body.String()
			for _, fragment := range []string{`lang="en"`, `name="viewport"`, `href="/static/site.css"`, `href="#main"`, `id="main"`, `class="site-footer"`, `href="https://lupricht.net/legal/"`, `href="https://lupricht.net/privacy/"`, "</html>"} {
				if !strings.Contains(body, fragment) {
					t.Errorf("missing shared element %s", fragment)
				}
			}
			if strings.Count(body, "<h1") != 1 || strings.Count(body, "<main") != 1 {
				t.Error("page must have exactly one main landmark and primary heading")
			}
			if strings.Contains(body, "<no value>") || strings.Contains(body, "ZgotmplZ") {
				t.Error("template rendering failed")
			}
		})
	}
}

func TestWebsiteCommandExamplesHaveIntentionalWhitespace(t *testing.T) {
	s, _ := testServer(t)
	for _, path := range []string{"/docs", "/how-to", "/account"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if path == "/account" {
			r.AddCookie(&http.Cookie{Name: "tpahub_session", Value: "tph_s_test"})
		}
		s.routes().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, w.Code)
		}
		body := w.Body.String()
		blocks := []string{}
		if path == "/docs" {
			blocks = append(blocks, "curl -fsSL https://tpa.run/tparun.sources", "tparun show https://tpa.run/r/eugen/dev-tools")
		}
		if path == "/how-to" {
			blocks = append(blocks, "curl -fsSL https://tpa.run/tparun.sources", "tparun show https://tpa.run/r/eugen/dev-tools")
		}
		if path == "/account" {
			blocks = append(blocks, "tparun login                 # uses the configured server\ntparun login tpa.run        # defaults to https://tpa.run")
		}
		for _, block := range blocks {
			if !strings.Contains(body, block) {
				t.Errorf("%s missing unindented command block %q", path, block)
			}
		}
		if strings.Contains(body, "\n            sudo ") || strings.Contains(body, "\n          tparun ") {
			t.Errorf("%s contains template indentation in a command block", path)
		}
	}
}

func TestWebsiteEcosystemExamples(t *testing.T) {
	s, _ := testServer(t)
	for _, path := range []string{"/", "/docs", "/how-to"} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, w.Code)
		}
		body := w.Body.String()
		fragments := []string{}
		if path == "/" {
			fragments = append(fragments, "APT package distribution", "Inspect repositories", "signed APT repositories")
		} else if path == "/docs" {
			fragments = append(fragments, "Reference and concepts", "Inspect repositories", "Repository trust")
		} else {
			fragments = append(fragments, "Task-oriented guide", "Inspect a repository before adding it", "Install a package")
		}
		for _, fragment := range fragments {
			if !strings.Contains(body, fragment) {
				t.Errorf("%s missing ecosystem example %q", path, fragment)
			}
		}
		if strings.Contains(body, "Try Bloom") {
			t.Errorf("%s retains the former Bloom CTA", path)
		}
	}
}

func TestWebsiteRepositorySafetyAndReleaseState(t *testing.T) {
	s, _ := testServer(t)
	for _, path := range []string{"/", "/docs", "/how-to"} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, w.Code)
		}
		body := w.Body.String()
		for _, unsafe := range []string{"Repos testen", "Try random repositories", "Add any repo and see what happens", "Get Started"} {
			if strings.Contains(body, unsafe) {
				t.Errorf("%s contains unsafe or vague repository CTA %q", path, unsafe)
			}
		}
		if path != "/" && !strings.Contains(body, "tparun show") {
			t.Errorf("%s does not provide an inspection command", path)
		}
	}
	for _, path := range []string{"/docs", "/how-to"} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if !strings.Contains(body, "tpa.run/tparun.sources") || !strings.Contains(body, "bootstrap") || !strings.Contains(body, "not active") {
			t.Errorf("%s does not state the current installation release state", path)
		}
	}
}

func TestWebsiteFormsKeepProductionContracts(t *testing.T) {
	s, _ := testServer(t)
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"/login", []string{`method="post" action="/login"`, `name="email"`, `name="password"`, `autocomplete="current-password"`}},
		{"/register", []string{`method="post" action="/register"`, `name="password_confirmation"`, `minlength="10"`, `autocomplete="new-password"`}},
		{"/ops/login", []string{`data-passkey-login`, `src="/static/ops-auth.js"`}},
		{"/ops/recover", []string{`method="post" action="/ops/recover"`, `data-recovery-form`, `name="recovery_code"`, `maxlength="256"`, `src="/static/ops-auth.js"`}},
	} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
		for _, fragment := range tc.want {
			if !strings.Contains(w.Body.String(), fragment) {
				t.Errorf("%s missing %s", tc.path, fragment)
			}
		}
	}
}

func TestWebsiteErrorRenderingEscapesInput(t *testing.T) {
	s, _ := testServer(t)
	for _, path := range []string{"/login", "/register"} {
		w := httptest.NewRecorder()
		s.webError(w, httptest.NewRequest(http.MethodGet, path, nil), `<script>alert("x")</script>`)
		body := w.Body.String()
		if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "&lt;script&gt;") || strings.Contains(body, "<script>alert") || !strings.Contains(body, "</html>") {
			t.Fatalf("%s: error not safely rendered", path)
		}
	}
}
