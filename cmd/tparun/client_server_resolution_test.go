package main

import "testing"

func TestResolveServerPrecedenceAndShorthand(t *testing.T) {
	tests := []struct{ name, explicit, configured, want string }{
		{"explicit wins", "packages.example.com", "https://tpa.run", "https://packages.example.com"},
		{"configured fallback", "", "tpa.run", "https://tpa.run"},
		{"port shorthand", "packages.example.com:8443", "", "https://packages.example.com:8443"},
		{"explicit https", "https://tpa.run", "http://localhost:8080", "https://tpa.run"},
		{"explicit http", "http://localhost:8080", "https://tpa.run", "http://localhost:8080"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveServer(test.explicit, test.configured)
			if err != nil || got != test.want {
				t.Fatalf("resolveServer()=%q,%v want %q", got, err, test.want)
			}
		})
	}
}

func TestResolveServerRejectsMissingAndMalformedValues(t *testing.T) {
	for _, test := range []struct{ explicit, configured string }{{"", ""}, {" ", "https://tpa.run"}, {"tpa.run ", ""}, {"://broken", ""}, {"https://", ""}, {"https:///foo", ""}} {
		if _, err := resolveServer(test.explicit, test.configured); err == nil {
			t.Fatalf("resolveServer(%q,%q) accepted malformed endpoint", test.explicit, test.configured)
		}
	}
}

func TestTemporaryServerOverrideDoesNotPersist(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	state := &clientState{Version: 2, Server: "https://tpa.run", Credentials: map[string]clientCredential{}}
	if err := saveCLI(cliConfig{Server: state.Server, state: state, SaveServer: true}); err != nil {
		t.Fatal(err)
	}
	selected := loadCLIForServer("packages.acme.internal")
	if selected.err != nil {
		t.Fatal(selected.err)
	}
	selected.SaveServer = false
	if err := saveCLI(selected); err != nil {
		t.Fatal(err)
	}
	stored := loadCLI()
	if stored.err != nil {
		t.Fatal(stored.err)
	}
	if stored.state.Server != "https://tpa.run" {
		t.Fatalf("temporary override persisted: %q", stored.state.Server)
	}
}
