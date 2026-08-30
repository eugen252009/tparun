package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/eugen252009/tpahub/internal/config"
	"golang.org/x/crypto/ssh/terminal"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type cliConfig struct{ Server, Email, Token string }

func configPath() string {
	d := os.Getenv("XDG_CONFIG_HOME")
	if d == "" {
		h, _ := os.UserHomeDir()
		d = filepath.Join(h, ".config")
	}
	return filepath.Join(d, "tpahub", "config.json")
}
func loadCLI() cliConfig {
	var c cliConfig
	b, e := os.ReadFile(configPath())
	if e == nil {
		_ = json.Unmarshal(b, &c)
	}
	if c.Server == "" {
		c.Server = "http://127.0.0.1:8080"
	}
	return c
}
func saveCLI(c cliConfig) error {
	p := configPath()
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, append(b, '\n'), 0600)
}
func input(prompt string, secret bool) string {
	fmt.Print(prompt)
	if secret {
		if b, e := terminal.ReadPassword(int(os.Stdin.Fd())); e == nil {
			fmt.Println()
			return string(b)
		}
	}
	v, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(v)
}
func cli(cfg config.Config, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Println("tpahub serve | register | login | logout | repo create/list/show/add/remove/token | publish | status")
		return nil
	}
	c := loadCLI()
	server := flagValue(args, "--server", c.Server)
	if server != "" {
		c.Server = strings.TrimRight(server, "/")
	}
	switch args[0] {
	case "register", "login":
		email := os.Getenv("TPAHUB_EMAIL")
		if email == "" {
			email = input("Email: ", false)
		}
		pw := os.Getenv("TPAHUB_PASSWORD")
		if pw == "" {
			pw = input("Password: ", true)
		}
		var out struct{ AccountID, Email, Token string }
		e := postJSON(c.Server+map[string]string{"register": "/v1/accounts", "login": "/v1/login"}[args[0]], map[string]string{"email": email, "password": pw}, &out)
		if e != nil {
			return e
		}
		c.Email = out.Email
		c.Token = out.Token
		return saveCLI(c)
	case "logout":
		if c.Token != "" {
			req, _ := http.NewRequest("POST", c.Server+"/v1/logout", nil)
			req.Header.Set("Authorization", "Bearer "+c.Token)
			_, _ = http.DefaultClient.Do(req)
		}
		c.Token = ""
		return saveCLI(c)
	case "repo":
		return repoCLI(c, args[1:])
	case "publish":
		if len(args) < 3 {
			return fmt.Errorf("usage: tpahub publish <repo> <files...>")
		}
		repo, e := owned(c, args[1])
		if e != nil {
			return e
		}
		var body bytes.Buffer
		w := newMultipart(&body)
		for _, p := range args[2:] {
			if e := w.AddFile("files", p); e != nil {
				return e
			}
		}
		w.Close()
		req, _ := http.NewRequest("POST", c.Server+"/v1/repositories/"+repo+"/publishes", &body)
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", w.FormDataContentType())
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			return e
		}
		return printResponse(res)
	case "status":
		if len(args) < 2 {
			return fmt.Errorf("job id required")
		}
		return getPrint(c.Server+"/v1/publish-jobs/"+args[1], c.Token)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
func flagValue(a []string, k, d string) string {
	for i, v := range a {
		if v == k && i+1 < len(a) {
			return a[i+1]
		}
		if strings.HasPrefix(v, k+"=") {
			return strings.SplitN(v, "=", 2)[1]
		}
		_ = i
	}
	return d
}
func postJSON(u string, v any, out any) error {
	b, _ := json.Marshal(v)
	r, e := http.Post(u, "application/json", bytes.NewReader(b))
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if r.StatusCode >= 300 {
		return fmt.Errorf("request failed: %s", r.Status)
	}
	return json.NewDecoder(r.Body).Decode(out)
}
func getPrint(u, t string) error {
	r, _ := http.NewRequest("GET", u, nil)
	if t != "" {
		r.Header.Set("Authorization", "Bearer "+t)
	}
	res, e := http.DefaultClient.Do(r)
	if e != nil {
		return e
	}
	return printResponse(res)
}
func printResponse(r *http.Response) error {
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode >= 300 {
		return fmt.Errorf("request failed: %s: %s", r.Status, strings.TrimSpace(string(b)))
	}
	fmt.Print(string(b))
	return nil
}
func owned(c cliConfig, v string) (string, error) {
	var r struct {
		ID string `json:"id"`
	}
	e := getJSON(c.Server+"/v1/repositories/"+url.PathEscape(v), c.Token, &r)
	return r.ID, e
}
func getJSON(u, t string, out any) error {
	r, _ := http.NewRequest("GET", u, nil)
	if t != "" {
		r.Header.Set("Authorization", "Bearer "+t)
	}
	res, e := http.DefaultClient.Do(r)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("request failed: %s", res.Status)
	}
	return json.NewDecoder(res.Body).Decode(out)
}
