package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type clientCredential struct {
	Email        string `json:"email"`
	Token        string `json:"token"`
	IdentityFile string `json:"identity_file,omitempty"`
}
type clientState struct {
	Version     int                         `json:"version"`
	Server      string                      `json:"server"`
	Credentials map[string]clientCredential `json:"credentials"`
	// A legacy token without an owning server is retained locally, never selected.
	Unbound *clientCredential `json:"unbound_legacy,omitempty"`
}

func webOrigin(u *url.URL) (string, error) {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if (scheme != "https" && scheme != "http") || host == "" || u.User != nil || u.Opaque != "" {
		return "", fmt.Errorf("server must be an explicit HTTP(S) origin without user information")
	}
	if scheme == "http" && host != "localhost" && !net.ParseIP(host).IsLoopback() && tparunEnv("TPARUN_ALLOW_PRIVATE_HTTP") != "true" {
		return "", fmt.Errorf("remote servers require HTTPS; HTTP is allowed only for loopback or explicit private development mode")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid server port")
		}
		port = strconv.Itoa(n)
	}
	if port == "443" && scheme == "https" || port == "80" && scheme == "http" {
		port = ""
	}
	authority := host
	if strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}
	if port != "" {
		authority = net.JoinHostPort(host, port)
	}
	return scheme + "://" + authority, nil
}
func resolveServer(explicit, configured string) (string, error) {
	selected := explicit
	if selected == "" {
		selected = configured
	}
	if selected == "" {
		return "", fmt.Errorf("No TPA.run server configured.\n\nProvide one explicitly:\n  tparun login tpa.run\n\nor configure a default server.")
	}
	return canonicalServer(selected)
}

func canonicalServer(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\t") {
		return "", fmt.Errorf("invalid server URL")
	}
	if strings.HasPrefix(raw, "://") || strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("invalid server URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid server URL")
	}
	if (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("server URL must contain only an origin")
	}
	return webOrigin(u)
}

func loadCLI() cliConfig { return loadCLIForServer("") }

func loadCLIForServer(explicit string) cliConfig {
	st := &clientState{Version: 2, Credentials: map[string]clientCredential{}}
	c := cliConfig{state: st, SaveServer: explicit != ""}
	b, err := os.ReadFile(configPath())
	if err != nil && !os.IsNotExist(err) {
		c.err = err
		return c
	}
	if err == nil {
		var version struct {
			Version int `json:"version"`
		}
		if err = json.Unmarshal(b, &version); err != nil {
			c.err = fmt.Errorf("invalid client configuration")
			return c
		}
		if version.Version == 0 {
			var old struct{ Server, Email, Token string }
			if err = json.Unmarshal(b, &old); err != nil {
				c.err = err
				return c
			}
			if old.Server != "" {
				origin, e := canonicalServer(old.Server)
				if e != nil {
					c.err = fmt.Errorf("legacy server cannot be scoped: %w", e)
					return c
				}
				st.Server = origin
				st.Credentials[origin] = clientCredential{Email: old.Email, Token: old.Token}
			} else if old.Token != "" {
				st.Unbound = &clientCredential{Email: old.Email, Token: old.Token}
			}
		} else if version.Version == 2 {
			if err = json.Unmarshal(b, st); err != nil {
				c.err = fmt.Errorf("invalid client configuration")
				return c
			}
			normalized := map[string]clientCredential{}
			for key, value := range st.Credentials {
				origin, e := canonicalServer(key)
				if e != nil {
					c.err = e
					return c
				}
				if _, exists := normalized[origin]; exists {
					c.err = fmt.Errorf("duplicate credential origin")
					return c
				}
				normalized[origin] = value
			}
			st.Credentials = normalized
		} else {
			c.err = fmt.Errorf("unsupported client configuration version")
			return c
		}
	}
	selected := st.Server
	if override := tparunEnv("TPARUN_SERVER"); override != "" {
		selected = override
	}
	resolved, resolveErr := resolveServer(explicit, selected)
	if resolveErr != nil {
		c.err = resolveErr
		return c
	}
	return c.selectServer(resolved)
}
func (c cliConfig) selectServer(raw string) cliConfig {
	if c.err != nil && !strings.HasPrefix(c.err.Error(), "No TPA.run server configured.") {
		return c
	}
	c.err = nil
	origin, err := canonicalServer(raw)
	if err != nil {
		c.err = err
		return c
	}
	c.Server = origin
	c.Email = ""
	c.Token = ""
	c.IdentityFile = ""
	if c.state != nil {
		credential := c.state.Credentials[origin]
		c.Email = credential.Email
		c.Token = credential.Token
		c.IdentityFile = credential.IdentityFile
	}
	return c
}
func saveCLI(c cliConfig) error {
	if c.err != nil {
		return c.err
	}
	origin, err := canonicalServer(c.Server)
	if err != nil {
		return err
	}
	st := c.state
	if st == nil {
		st = &clientState{Version: 2, Credentials: map[string]clientCredential{}}
	}
	if c.SaveServer {
		st.Server = origin
	}
	if c.Token == "" && c.IdentityFile == "" {
		delete(st.Credentials, origin)
	} else {
		st.Credentials[origin] = clientCredential{Email: c.Email, Token: c.Token, IdentityFile: c.IdentityFile}
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	path := configPath()
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// One transport policy for CLI API requests, including anonymous discovery,
// password-bearing login and replayable publication bodies. Never select a new
// credential on redirect, even if that origin has a saved credential.
func doCLIRequest(req *http.Request) (*http.Response, error) {
	origin, err := webOrigin(req.URL)
	if err != nil {
		return nil, err
	}
	client := *http.DefaultClient
	client.Jar = nil
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		target, e := webOrigin(next.URL)
		if e != nil || target != origin {
			return fmt.Errorf("cross-origin redirect refused")
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}
	return client.Do(req)
}
