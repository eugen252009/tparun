package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/eugen252009/tpahub/internal/config"
	"golang.org/x/crypto/ssh/terminal"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
	appversion "github.com/eugen252009/tpahub/internal/version"
)

type cliConfig struct{ Server, Email, Token string }

const defaultServer = "https://tpa.lupricht.net"

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
		c.Server = defaultServer
	}
	if override := os.Getenv("TPAHUB_SERVER"); override != "" {
		c.Server = strings.TrimRight(override, "/")
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
		fmt.Println("tpahub serve | schema | migrate | register | login | logout | repo create/list/show/add/remove/token | publish | status | admin egress ... | admin account classify ... | admin operator recovery issue|revoke")
		return nil
	}
	if args[0] == "admin" {
		return adminCLI(cfg, args[1:])
	}
	c := loadCLI()
	server := flagValue(args, "--server", c.Server)
	if server != "" {
		c.Server = strings.TrimRight(server, "/")
	}
	switch args[0] {
	case "schema":
		fmt.Println(store.CurrentSchemaVersion())
		return nil
	case "version":
		fmt.Println(appversion.Current)
		return nil
	case "digest":
		path := flagValue(args, "--db", filepath.Join(cfg.DataDir, "tpahub.db"))
		d, err := store.LogicalDigest(path)
		if err != nil {
			return err
		}
		fmt.Println(d)
		return nil
	case "migrate":
		return store.Migrate(cfg.DataDir)
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

func adminCLI(cfg config.Config, args []string) error {
	if len(args) < 2 || (args[0] != "egress" && args[0] != "account" && args[0] != "operator") {
		return fmt.Errorf("usage: tpahub admin egress list-uncertain | inspect <delivery-id> | recover <delivery-id> --version <version> --usage-at <RFC3339> --bytes <bytes> | account classify <account-id> --class <production|test|internal|unclassified>")
	}
	dataDir := flagValue(args, "--data-dir", cfg.DataDir)
	if args[0] == "operator" && args[1] == "recovery" {
		if flagValue(args, "--data-dir", "") == "" && os.Getenv("TPAHUB_DATA_DIR") == "" {
			return fmt.Errorf("operator recovery requires explicit --data-dir or TPAHUB_DATA_DIR")
		}
		fmt.Fprintf(os.Stderr, "TPA Hub data directory: %s\n", dataDir)
		if _, err := os.Stat(filepath.Join(dataDir, "tpahub.db")); err != nil {
			return fmt.Errorf("TPA Hub database not found at %s: %w", dataDir, err)
		}
	}
	db, err := store.OpenForAdmin(dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	switch args[1] {
	case "recovery":
		if args[0] != "operator" || len(args) < 3 {
			return fmt.Errorf("usage: tpahub admin operator recovery issue|revoke")
		}
		switch args[2] {
		case "issue":
			secret := make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return err
			}
			code := "TPA-RCV-" + base64.RawURLEncoding.EncodeToString(secret)
			hash := sha256.Sum256([]byte(code))
			now := time.Now().UTC()
			if err := db.IssueOperatorRecovery(ctx, hash[:], now.Format(time.RFC3339Nano), now.Add(15*time.Minute).Format(time.RFC3339Nano)); err != nil {
				return err
			}
			fmt.Printf("Emergency operator recovery code:\n\n%s\n\nExpires: %s\nValid for one recovery attempt only.\n", code, now.Add(15*time.Minute).Format(time.RFC3339))
			return nil
		case "revoke":
			if err := db.RevokeOperatorRecovery(ctx); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, "operator recovery revoked")
			return nil
		default:
			return fmt.Errorf("usage: tpahub admin operator recovery issue|revoke")
		}
	case "classify":
		if args[0] != "account" || len(args) < 3 {
			return fmt.Errorf("usage: tpahub admin account classify <account-id> --class <production|test|internal|unclassified>")
		}
		if err := db.SetAccountClass(ctx, args[2], flagValue(args, "--class", "")); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "account classification updated")
		return nil
	case "list-uncertain":
		all, err := db.EgressDeliveries(ctx, "")
		if err != nil {
			return fmt.Errorf("list uncertain egress: %w", err)
		}
		deliveries := make([]store.EgressDelivery, 0, len(all))
		for _, delivery := range all {
			if delivery.State == "uncertain" {
				deliveries = append(deliveries, delivery)
			}
		}
		return json.NewEncoder(os.Stdout).Encode(deliveries)
	case "inspect":
		if len(args) < 3 || args[2] == "" {
			return fmt.Errorf("delivery id required")
		}
		delivery, err := adminEgressDelivery(ctx, db, args[2])
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(delivery)
	case "recover":
		if len(args) < 3 || args[2] == "" {
			return fmt.Errorf("delivery id required")
		}
		delivery, err := adminEgressDelivery(ctx, db, args[2])
		if err != nil {
			return err
		}
		version, err := strconv.ParseInt(flagValue(args, "--version", ""), 10, 64)
		if err != nil || version <= 0 {
			return fmt.Errorf("positive marker version required")
		}
		bytes, err := strconv.ParseInt(flagValue(args, "--bytes", ""), 10, 64)
		if err != nil || bytes < 0 {
			return fmt.Errorf("non-negative accounted bytes required")
		}
		usageDate := flagValue(args, "--usage-at", "")
		if _, parseErr := time.Parse(time.RFC3339Nano, usageDate); parseErr != nil {
			return fmt.Errorf("usage instant must be RFC3339")
		}
		operator := os.Getenv("TPAHUB_OPERATOR")
		if operator == "" {
			operator = os.Getenv("USER")
		}
		if operator == "" {
			operator = "cli"
		}
		if err = db.RecoverEgressDeliveryWithAudit(ctx, delivery.AccountID, delivery.ID, delivery.RepositoryID, version, usageDate, bytes, time.Now().UTC().Format(time.RFC3339Nano), operator); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"delivery_id": delivery.ID, "result": "recovered"})
	default:
		return fmt.Errorf("unknown egress admin command %q", args[1])
	}
}

func adminEgressDelivery(ctx context.Context, db *store.Store, id string) (store.EgressDelivery, error) {
	deliveries, err := db.EgressDeliveries(ctx, "")
	if err != nil {
		return store.EgressDelivery{}, fmt.Errorf("inspect uncertain egress: %w", err)
	}
	for _, delivery := range deliveries {
		if delivery.ID == id {
			return delivery, nil
		}
	}
	return store.EgressDelivery{}, fmt.Errorf("egress delivery %q not found", id)
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
