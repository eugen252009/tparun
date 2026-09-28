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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
	appversion "github.com/eugen252009/tpahub/internal/version"
)

type cliConfig struct {
	Server, Email, Token, IdentityFile string
	SaveServer                         bool
	state                              *clientState
	err                                error
}

func configPath() string {
	d := os.Getenv("XDG_CONFIG_HOME")
	if d == "" {
		h, _ := os.UserHomeDir()
		d = filepath.Join(h, ".config")
	}
	return filepath.Join(d, "tparun", "config.json")
}

var (
	lineInputFile   *os.File
	lineInputReader *bufio.Reader
)

func input(prompt string, secret bool) string {
	fmt.Print(prompt)
	if secret {
		if b, e := terminal.ReadPassword(int(os.Stdin.Fd())); e == nil {
			fmt.Println()
			return string(b)
		}
	}
	if lineInputFile != os.Stdin {
		lineInputFile = os.Stdin
		lineInputReader = bufio.NewReader(os.Stdin)
	}
	v, _ := lineInputReader.ReadString('\n')
	return strings.TrimSpace(v)
}

func readRepositoryPassword() (string, error) {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return "", fmt.Errorf("private repository requires an interactive terminal; use --token for automation")
	}
	defer tty.Close()
	fmt.Fprint(os.Stderr, "Repository password: ")
	value, err := terminal.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading repository password: %w", err)
	}
	return strings.TrimSpace(string(value)), nil
}
func cli(cfg config.Config, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Println("TPA.run CLI (tparun)")
		fmt.Println("Repositories: list (account-owned; requires login) | add <repo-url> | remove <repo-url> | installed (local APT sources)")
		fmt.Println("Publication workspace: stage <package.deb>... | unstage <package.deb> | status | publish [--yes] [--verbose] <repo-url>")
		fmt.Println("Remote repositories: create <name> | show <repo-url> | edit <repo-url> visibility | delete <repo-url>")
		fmt.Println("Identity: account    Authentication: register [server] | login [server] | login --ssh [identity-file] | logout    System: version | --version | doctor")
		fmt.Println("Operator diagnostics: admin publication-status --cookie-file <path> [--json]")
		fmt.Println("Server names without a scheme default to HTTPS; explicit server arguments override configuration.")
		return nil
	}
	if args[0] == "publish" && has(args[1:], "--help") {
		fmt.Println("Usage: tparun publish [--yes] [--verbose] [--no-wait] <repository-url>")
		fmt.Println("Publish the currently staged packages to a repository.")
		fmt.Println("Options:")
		fmt.Println("  -y, --yes      Skip confirmation")
		fmt.Println("      --no-wait  Queue publication and return its ID")
		fmt.Println("      --verbose  Show the detailed publication plan")
		return nil
	}
	if args[0] == "installed" {
		return installedCLI(args[1:])
	}
	if args[0] == "stage" {
		return stageCLI(args[1:])
	}
	if args[0] == "unstage" {
		return unstageCLI(args[1:])
	}
	if args[0] == "status" && len(args) == 1 {
		return statusCLI()
	}
	if args[0] == "init" {
		if len(args) != 1 {
			return fmt.Errorf("usage: tparun init (TPARUN_DATA_DIR selects the new directory)")
		}
		lock, err := lockDataDirectory(cfg.DataDir)
		if err != nil {
			return err
		}
		defer lock.Close()
		if err = config.InitializeSelfHosted(cfg.DataDir); err != nil {
			return err
		}
		fmt.Println("Initialized self-hosted profile defaults: metrics-only delivery, billing none. Explicit environment settings take precedence. Run tparun serve with the same TPARUN_DATA_DIR.")
		return nil
	}
	if args[0] == "admin" {
		return adminCLI(cfg, args[1:])
	}
	// Local state/version commands must not depend on client credentials.
	switch args[0] {
	case "diagnostics", "doctor":
		return diagnostics()
	case "schema":
		fmt.Println(store.CurrentSchemaVersion())
		return nil
	case "version", "--version":
		fmt.Println(appversion.Current)
		return nil
	case "digest":
		d, err := store.LogicalDigest(flagValue(args, "--db", filepath.Join(cfg.DataDir, "tpahub.db")))
		if err != nil {
			return err
		}
		fmt.Println(d)
		return nil
	case "migrate":
		return store.Migrate(cfg.DataDir)
	}
	for i, arg := range args {
		if arg == "--server=" || (arg == "--server" && (i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--"))) {
			return fmt.Errorf("--server requires an explicit origin")
		}
	}
	flagServer := flagValue(args, "--server", "")
	serverFlagCount := 0
	for _, arg := range args {
		if arg == "--server" || strings.HasPrefix(arg, "--server=") {
			serverFlagCount++
		}
	}
	if serverFlagCount > 1 {
		return fmt.Errorf("server specified more than once")
	}
	positionalServer := ""
	if (args[0] == "login" || args[0] == "register") && len(args) > 1 && args[1] != "--ssh" && !strings.HasPrefix(args[1], "--") {
		positionalServer = args[1]
	}
	if flagServer != "" && positionalServer != "" {
		return fmt.Errorf("server specified both positionally and with --server")
	}
	explicitServer := flagServer
	if explicitServer == "" {
		explicitServer = positionalServer
	}
	c := loadCLIForServer(explicitServer)
	c.SaveServer = args[0] == "login" || args[0] == "register"
	if c.err != nil {
		return c.err
	}
	var operands []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--server" {
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--server=") {
			continue
		}
		operands = append(operands, args[i])
	}
	args = operands
	if positionalServer != "" && len(args) > 1 && (args[0] == "login" || args[0] == "register") {
		args = append(args[:1], args[2:]...)
	}
	if len(args) == 0 {
		return fmt.Errorf("command required after --server")
	}
	if len(args) == 0 {
		return fmt.Errorf("client command required")
	}
	if c.Token == "" && c.IdentityFile != "" && args[0] != "login" && args[0] != "register" && args[0] != "logout" {
		updated, err := sshLoginCLI(c, c.IdentityFile, true)
		if err != nil {
			return fmt.Errorf("automatic SSH session renewal failed: %w", err)
		}
		if err = saveCLI(updated); err != nil {
			return err
		}
		c = updated
	}
	switch args[0] {
	case "list":
		return listRepositoriesCLI(c, args[1:])
	case "register", "login":
		if args[0] == "login" && len(args) >= 2 && args[1] == "--ssh" {
			nonInteractive := has(args, "--non-interactive")
			operands := make([]string, 0, len(args)-2)
			for _, arg := range args[2:] {
				if arg != "--non-interactive" {
					operands = append(operands, arg)
				}
			}
			if len(operands) > 1 {
				return fmt.Errorf("usage: tparun login --ssh [identity-file] [--non-interactive]")
			}
			identity := ""
			if len(operands) == 1 {
				identity = operands[0]
			}
			updated, err := sshLoginCLI(c, identity, nonInteractive)
			if err != nil {
				return err
			}
			return saveCLI(updated)
		}
		email := tparunEnv("TPARUN_EMAIL")
		if email == "" {
			email = input("Email: ", false)
		}
		pw := tparunEnv("TPARUN_PASSWORD")
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
			res, err := doCLIRequest(req)
			if err != nil {
				return err
			}
			res.Body.Close()
			if res.StatusCode >= 300 {
				return fmt.Errorf("logout failed: %s", res.Status)
			}
		}
		c.Token = ""
		return saveCLI(c)
	case "add", "remove", "show", "create", "edit", "delete":
		return repositoryCommand(c, args)
	case "account":
		if len(args) >= 2 && args[1] == "credentials" {
			return credentialsCLI(c, args[2:])
		}
		if len(args) != 1 {
			return fmt.Errorf("usage: tparun account [credentials]")
		}
		if c.Token == "" {
			return fmt.Errorf("authentication required; run tparun login")
		}
		var out struct {
			ID, Email, AccountClass, StorageLimitState  string
			RepositoryCount, PackageCount, StorageBytes int64
			StorageLimitBytes                           int64
		}
		if err := getJSONWithSSHReauth(c, c.Server+"/v1/account", &out); err != nil {
			return err
		}
		fmt.Printf("ID: %s\nEmail: %s\nClass: %s\nRepositories: %d\nPackages: %d\n", out.ID, out.Email, out.AccountClass, out.RepositoryCount, out.PackageCount)
		if out.StorageLimitState == "finite" {
			fmt.Printf("Storage: %s / %s\n", formatBytes(out.StorageBytes), formatBytes(out.StorageLimitBytes))
		} else {
			fmt.Printf("Storage: %s\n", formatBytes(out.StorageBytes))
		}
		return nil
	case "publish":
		yes := has(args, "-y") || has(args, "--yes")
		verbose := has(args, "--verbose")
		noWait := has(args, "--no-wait")
		operands := make([]string, 0, len(args))
		for _, arg := range args[1:] {
			if arg != "-y" && arg != "--yes" && arg != "--verbose" && arg != "--no-wait" {
				operands = append(operands, arg)
			}
		}
		if len(operands) != 1 {
			return fmt.Errorf("usage: tparun publish [--yes] [--verbose] [--no-wait] <repo-url>")
		}
		return publishStaged(c, operands[0], yes, verbose, noWait)
	case "check":
		if len(args) < 2 {
			return fmt.Errorf("usage: tparun check <publication-id> [sha256] [--wait] [--timeout <duration>]")
		}
		wait := has(args, "--wait")
		asJSON := has(args, "--json")
		timeout := 10 * time.Minute
		operands := make([]string, 0, len(args))
		for i := 1; i < len(args); i++ {
			arg := args[i]
			if arg == "--wait" || arg == "--json" {
				continue
			}
			if arg == "--timeout" {
				if i+1 < len(args) {
					i++
				}
				continue
			}
			operands = append(operands, arg)
		}
		for i, arg := range args {
			if arg == "--timeout" && i+1 < len(args) {
				var err error
				timeout, err = time.ParseDuration(args[i+1])
				if err != nil || timeout <= 0 {
					return fmt.Errorf("invalid timeout")
				}
				break
			}
		}
		if len(operands) < 1 || len(operands) > 2 {
			return fmt.Errorf("usage: tparun check <publication-id> [sha256] [--wait]")
		}
		sha := ""
		if len(operands) == 2 {
			sha = operands[1]
		}
		return checkPublication(c, operands[0], sha, wait, timeout, asJSON)
	case "status":
		if len(args) < 2 {
			return statusCLI()
		}
		return getPrint(c.Server+"/v1/publish-jobs/"+args[1], c.Token)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func adminCLI(cfg config.Config, args []string) error {
	if len(args) > 0 && args[0] == "publication-status" {
		return adminPublicationStatusCLI(args[1:])
	}
	if len(args) < 2 || (args[0] != "egress" && args[0] != "account" && args[0] != "operator" && args[0] != "bootstrap" && args[0] != "repository") {
		return fmt.Errorf("usage: tparun admin publication-status --cookie-file <path> [--server <origin>] [--json] | repository ... | bootstrap ... | egress ... | account ... | operator ...")
	}
	dataDir := flagValue(args, "--data-dir", cfg.DataDir)
	if args[0] == "operator" && args[1] == "recovery" {
		if flagValue(args, "--data-dir", "") == "" && tparunEnv("TPARUN_DATA_DIR") == "" {
			return fmt.Errorf("operator recovery requires explicit --data-dir or TPARUN_DATA_DIR")
		}
		fmt.Fprintf(os.Stderr, "TPA.run data directory: %s\n", dataDir)
		if _, err := os.Stat(filepath.Join(dataDir, "tpahub.db")); err != nil {
			return fmt.Errorf("TPA.run database not found at %s: %w", dataDir, err)
		}
	}
	db, err := store.OpenForAdmin(dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	switch args[1] {
	case "transfer-owner":
		if args[0] != "repository" || len(args) < 4 {
			return fmt.Errorf("usage: tparun admin repository transfer-owner <repository-id> <account-id>")
		}
		op := tparunEnv("TPARUN_OPERATOR")
		if op == "" {
			op = os.Getenv("USER")
		}
		if op == "" {
			op = "cli"
		}
		if err := db.TransferRepositoryOwner(ctx, args[2], args[3], op, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "repository owner transferred")
		return nil
	case "grant", "revoke":
		if args[0] != "repository" || len(args) < 5 || args[4] != store.RepositoryPublishDelegation {
			return fmt.Errorf("usage: tparun admin repository grant|revoke <repository-id> <account-id> repository.publish")
		}
		op := tparunEnv("TPARUN_OPERATOR")
		if op == "" {
			op = os.Getenv("USER")
		}
		if op == "" {
			op = "cli"
		}
		enabled := args[1] == "grant"
		if err := db.SetRepositoryDelegation(ctx, args[3], args[2], args[4], op, time.Now().UTC().Format(time.RFC3339Nano), enabled); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "repository delegation %s\n", map[bool]string{true: "granted", false: "revoked"}[enabled])
		return nil
	case "promote":
		if args[0] != "bootstrap" || len(args) < 4 {
			return fmt.Errorf("usage: tparun admin bootstrap promote <repository-id> <artifact-path>")
		}
		op := tparunEnv("TPARUN_OPERATOR")
		if op == "" {
			op = os.Getenv("USER")
		}
		if op == "" {
			op = "cli"
		}
		promotion, err := promoteBootstrap(ctx, db, dataDir, args[2], args[3], op, time.Now().UTC())
		if err != nil {
			return err
		}
		fmt.Printf("Promoted bootstrap %s %s sha256=%s\n", promotion.Package, promotion.Version, promotion.SHA256)
		return nil
	case "recovery":
		if args[0] != "operator" || len(args) < 3 {
			return fmt.Errorf("usage: tparun admin operator recovery issue|revoke")
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
			return fmt.Errorf("usage: tparun admin operator recovery issue|revoke")
		}
	case "enable":
		if args[0] != "bootstrap" || len(args) < 3 {
			return fmt.Errorf("usage: tparun admin bootstrap enable <repository-id>")
		}
		op := tparunEnv("TPARUN_OPERATOR")
		if op == "" {
			op = os.Getenv("USER")
		}
		if op == "" {
			op = "cli"
		}
		if err := db.EnableGlobalBootstrap(ctx, args[2], op, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "global bootstrap repository enabled")
		return nil
	case "capacity":
		if args[0] != "bootstrap" || len(args) < 4 {
			return fmt.Errorf("usage: tparun admin bootstrap capacity <storage-bytes> <egress-bytes>")
		}
		storage, err := parseFiniteCapacity(args[2])
		if err != nil {
			return fmt.Errorf("storage capacity: %w", err)
		}
		egress, err := parseFiniteCapacity(args[3])
		if err != nil {
			return fmt.Errorf("egress capacity: %w", err)
		}
		b, found, err := db.GlobalBootstrapRepository(ctx)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("no global bootstrap repository")
		}
		op := tparunEnv("TPARUN_OPERATOR")
		if op == "" {
			op = os.Getenv("USER")
		}
		if op == "" {
			op = "cli"
		}
		if err := db.SetBootstrapCapacity(ctx, b.AccountID, b.RepositoryID, op, storage, egress, "operator-selected bootstrap capacity", time.Now().UTC()); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "global bootstrap capacity updated")
		return nil
	case "classify":
		if args[0] != "account" || len(args) < 3 {
			return fmt.Errorf("usage: tparun admin account classify <account-id> --class <production|test|internal|unclassified>")
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
		operator := tparunEnv("TPARUN_OPERATOR")
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
func redactSecrets(value string) string {
	for _, prefix := range []string{"tph_s_", "tph_r_"} {
		for {
			i := strings.Index(value, prefix)
			if i < 0 {
				break
			}
			j := i
			for j < len(value) && !strings.ContainsRune(" \t\r\n\\\"'", rune(value[j])) {
				j++
			}
			value = value[:i] + "<redacted>" + value[j:]
		}
	}
	return strings.ReplaceAll(value, "Authorization: Bearer ", "Authorization: Bearer <redacted>")
}

func postJSON(u string, v any, out any) error {
	b, _ := json.Marshal(v)
	req, e := http.NewRequest("POST", u, bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	r, e := doCLIRequest(req)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if r.StatusCode >= 300 {
		body, _ := io.ReadAll(r.Body)
		message := strings.TrimSpace(string(body))
		if message != "" {
			return fmt.Errorf("request failed: %s: %s", r.Status, redactSecrets(message))
		}
		return fmt.Errorf("request failed: %s", r.Status)
	}
	return json.NewDecoder(r.Body).Decode(out)
}
func getPrint(u, t string) error {
	r, _ := http.NewRequest("GET", u, nil)
	if t != "" {
		r.Header.Set("Authorization", "Bearer "+t)
	}
	res, e := doCLIRequest(r)
	if e != nil {
		return e
	}
	return printResponse(res)
}
func printResponse(r *http.Response) error {
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode >= 300 {
		return fmt.Errorf("request failed: %s: %s", r.Status, redactSecrets(strings.TrimSpace(string(b))))
	}
	fmt.Print(redactSecrets(string(b)))
	return nil
}
func getJSON(u, t string, out any) error {
	r, _ := http.NewRequest("GET", u, nil)
	if t != "" {
		r.Header.Set("Authorization", "Bearer "+t)
	}
	res, e := doCLIRequest(r)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("request failed: %s", res.Status)
	}
	return json.NewDecoder(res.Body).Decode(out)
}
