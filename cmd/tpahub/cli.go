package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/eugen252009/tpahub/internal/config"
)

func cli(cfg config.Config, args []string) error {
	if len(args) < 2 || args[0] != "repo" {
		return fmt.Errorf("usage: tpahub repo add|remove <repository-id> [--server URL]")
	}
	action := args[1]
	if len(args) < 3 {
		return fmt.Errorf("repository id is required")
	}
	id := args[2]
	server := os.Getenv("TPAHUB_SERVER")
	if server == "" {
		server = "http://127.0.0.1:8080"
	}
	for i := 3; i < len(args); i++ {
		if args[i] == "--server" && i+1 < len(args) {
			server = args[i+1]
			i++
		} else if strings.HasPrefix(args[i], "--server=") {
			server = strings.TrimPrefix(args[i], "--server=")
		}
	}
	if !validID(id) {
		return fmt.Errorf("invalid repository id")
	}
	base := strings.TrimRight(server, "/")
	name := "tpahub-" + id
	source := filepath.Join("/etc/apt/sources.list.d", name+".list")
	key := filepath.Join("/etc/apt/keyrings", name+".gpg")
	switch action {
	case "add":
		resp, e := http.Get(base + "/v1/repositories/" + url.PathEscape(id) + "/signing-key")
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("signing key endpoint returned %s", resp.Status)
		}
		keyData, e := io.ReadAll(resp.Body)
		if e != nil {
			return e
		}
		if e = os.MkdirAll(filepath.Dir(key), 0755); e != nil {
			return e
		}
		if e = os.WriteFile(key, keyData, 0644); e != nil {
			return e
		}
		line := fmt.Sprintf("deb [signed-by=%s] %s/r/%s/ stable main\n", key, base, id)
		if old, e := os.ReadFile(source); e == nil && string(old) == line {
			return nil
		}
		if e = os.WriteFile(source, []byte(line), 0644); e != nil {
			return e
		}
		fmt.Printf("configured %s\n", base+"/r/"+id+"/")
		return nil
	case "remove":
		_ = os.Remove(source)
		_ = os.Remove(key)
		return nil
	default:
		return fmt.Errorf("unknown repo action %q", action)
	}
}
func validID(s string) bool { return idRE.MatchString(s) }
