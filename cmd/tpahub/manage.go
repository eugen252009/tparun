package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type multipartWriter struct{ *multipart.Writer }

func newMultipart(b *bytes.Buffer) *multipartWriter { return &multipartWriter{multipart.NewWriter(b)} }
func (w *multipartWriter) AddFile(field, path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	p := filepath.Base(path)
	x, e := w.CreateFormFile(field, p)
	if e != nil {
		return e
	}
	_, e = io.Copy(x, f)
	return e
}
func repoCLI(c cliConfig, a []string) error {
	if len(a) < 1 {
		return fmt.Errorf("repo command required")
	}
	switch a[0] {
	case "create":
		if len(a) < 2 {
			return fmt.Errorf("repository name required")
		}
		v := map[string]string{"name": a[1], "visibility": "private"}
		if has(a, "--public") {
			v["visibility"] = "public"
		}
		var out any
		return postAuth(c, "/v1/repositories", v, &out)
	case "list":
		return getPrint(c.Server+"/v1/repositories", c.Token)
	case "show":
		if len(a) < 2 {
			return fmt.Errorf("repository required")
		}
		return getPrint(c.Server+"/v1/repositories/"+a[1], c.Token)
	case "add":
		if len(a) < 2 {
			return fmt.Errorf("repository required")
		}
		return addRepo(c, a[1], flagValue(a, "--token", ""))
	case "remove":
		if len(a) < 2 {
			return fmt.Errorf("repository required")
		}
		return removeRepo(c, a[1])
	case "token":
		return tokenCLI(c, a[1:])
	default:
		return fmt.Errorf("unknown repo command %q", a[0])
	}
}
func has(a []string, v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}
func postAuth(c cliConfig, path string, v any, out any) error {
	b, _ := json.Marshal(v)
	r, _ := http.NewRequest("POST", c.Server+path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+c.Token)
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
func tokenCLI(c cliConfig, a []string) error {
	if len(a) < 2 {
		return fmt.Errorf("token create|list|revoke <repo> ...")
	}
	repo, e := owned(c, a[1])
	if e != nil {
		return e
	}
	switch a[0] {
	case "create":
		if len(a) < 3 {
			return fmt.Errorf("token name required")
		}
		var out map[string]any
		e = postAuth(c, "/v1/repositories/"+repo+"/reader-credentials", map[string]string{"name": a[2]}, &out)
		if e == nil {
			fmt.Printf("Token: %v\n", out["token"])
		}
		return e
	case "list":
		return getPrint(c.Server+"/v1/repositories/"+repo+"/reader-credentials", c.Token)
	case "revoke":
		if len(a) < 3 {
			return fmt.Errorf("token id required")
		}
		req, _ := http.NewRequest("DELETE", c.Server+"/v1/repositories/"+repo+"/reader-credentials/"+a[2], nil)
		req.Header.Set("Authorization", "Bearer "+c.Token)
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			return e
		}
		return printResponse(res)
	default:
		return fmt.Errorf("unknown token action")
	}
}
func addRepo(c cliConfig, v, reader string) error {
	repo, e := owned(c, v)
	if e != nil {
		repo = v
	}
	var meta struct{ ID, Visibility string }
	if reader != "" {
		meta.ID = repo
		meta.Visibility = "private"
	} else if e = getJSON(c.Server+"/v1/repositories/"+repo, c.Token, &meta); e != nil {
		return e
	}
	key, e := http.Get(c.Server + "/v1/repositories/" + repo + "/signing-key")
	if e != nil {
		return e
	}
	defer key.Body.Close()
	kb, _ := io.ReadAll(key.Body)
	name := "tpahub-" + repo
	keyPath := "/etc/apt/keyrings/" + name + ".gpg"
	src := "/etc/apt/sources.list.d/" + name + ".list"
	if e = os.MkdirAll("/etc/apt/keyrings", 0755); e != nil {
		return e
	}
	if e = os.WriteFile(keyPath, kb, 0644); e != nil {
		return e
	}
	if meta.Visibility == "private" {
		if reader == "" {
			return fmt.Errorf("private repository requires --token")
		}
		auth := "machine " + strings.TrimRight(c.Server, "/") + " login reader password " + reader + "\n"
		if e = os.MkdirAll("/etc/apt/auth.conf.d", 0755); e != nil {
			return e
		}
		if e = os.WriteFile("/etc/apt/auth.conf.d/"+name, []byte(auth), 0600); e != nil {
			return e
		}
	}
	line := fmt.Sprintf("deb [signed-by=%s] %s/r/%s/ stable main\n", keyPath, c.Server, repo)
	return os.WriteFile(src, []byte(line), 0644)
}
func removeRepo(c cliConfig, v string) error {
	repo, e := owned(c, v)
	if e != nil {
		repo = v
	}
	name := "tpahub-" + repo
	for _, p := range []string{"/etc/apt/sources.list.d/" + name + ".list", "/etc/apt/keyrings/" + name + ".gpg", "/etc/apt/auth.conf.d/" + name} {
		_ = os.Remove(p)
	}
	return nil
}
