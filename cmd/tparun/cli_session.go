package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// sshReauthenticate is injectable so request replay and retry limits can be
// tested without depending on a real SSH key or service.
var sshReauthenticate = func(c cliConfig) (cliConfig, error) {
	if c.IdentityFile == "" {
		return c, fmt.Errorf("SSH identity is not configured")
	}
	c.Token = ""
	if err := saveCLI(c); err != nil {
		return c, err
	}
	updated, err := sshLoginCLI(c, c.IdentityFile, true)
	if err != nil {
		return c, err
	}
	if err := saveCLI(updated); err != nil {
		return c, err
	}
	return updated, nil
}

func sessionAuthFailure(status int) bool { return status == http.StatusUnauthorized }

func authenticatedJSONRequest(c cliConfig, method, u string, body []byte, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest(method, u, bytes.NewReader(body))
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		res, err := doCLIRequest(req)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		if readErr != nil {
			return readErr
		}
		if res.StatusCode < 300 {
			if out == nil || len(data) == 0 {
				return nil
			}
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decode request response (%s): %s", res.Status, redactSecrets(string(data)))
			}
			return nil
		}
		if sessionAuthFailure(res.StatusCode) && attempt == 0 && c.IdentityFile != "" && c.Token != "" {
			updated, authErr := sshReauthenticate(c)
			if authErr != nil {
				return fmt.Errorf("SSH session re-authentication failed: %s", redactSecrets(authErr.Error()))
			}
			c = updated
			continue
		}
		message := redactSecrets(string(data))
		if message != "" {
			return fmt.Errorf("request failed: %s: %s", res.Status, message)
		}
		return fmt.Errorf("request failed: %s", res.Status)
	}
	return fmt.Errorf("request authentication retry exhausted")
}

func getJSONWithSSHReauth(c cliConfig, u string, out any) error {
	return authenticatedJSONRequest(c, http.MethodGet, u, nil, out)
}
