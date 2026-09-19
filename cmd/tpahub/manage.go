package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
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

var repositoryPrompt = func(prompt string) string { return input(prompt, false) }

func repositoryCommand(c cliConfig, a []string) error {
	if len(a) < 1 {
		return fmt.Errorf("repo command required")
	}
	switch a[0] {
	case "create":
		if len(a) < 2 {
			return fmt.Errorf("repository name required")
		}
		if has(a, "--public") {
			return fmt.Errorf("--public is removed; create private, then use tpahub edit <repo-url> visibility")
		}
		v := map[string]string{"name": a[1], "visibility": "private"}
		var out map[string]any
		e := postAuth(c, "/v1/repositories", v, &out)
		if e == nil {
			name, _ := out["name"].(string)
			username, _ := out["username"].(string)
			fmt.Println(canonicalRepositoryURL(c.Server, username, name))
		}
		return e
	case "show":
		if len(a) < 2 {
			return fmt.Errorf("repository URL required")
		}
		target, repository, err := repositoryTarget(c, a[1])
		if err != nil {
			return err
		}
		var metadata repositoryView
		if _, err := getRepositoryMetadata(target, repository, &metadata); err != nil {
			return err
		}
		fmt.Printf("Repository: %s\nVisibility: %s\n", canonicalRepositoryURL(target.Server, metadata.Username, metadata.Name), metadata.Visibility)
		return nil
	case "add":
		if len(a) > 1 && (a[1] == "--help" || a[1] == "-h") {
			fmt.Println("Usage: tpahub add <repo-url> [--dry-run]")
			fmt.Println("Public repositories are added directly; private repositories prompt for a repository password.")
			fmt.Println("For automation, --token supplies an existing reader credential without prompting.")
			return nil
		}
		if len(a) < 2 {
			return fmt.Errorf("repository reference required")
		}
		target, repository, err := repositoryTarget(c, a[1])
		if err != nil {
			return err
		}
		return addRepo(target, repository, flagValue(a, "--token", ""), has(a, "--dry-run"))
	case "remove":
		if len(a) < 2 {
			return fmt.Errorf("repository URL required")
		}
		target, repository, err := repositoryTarget(c, a[1])
		if err != nil {
			return err
		}
		return removeRepo(target, repository)
	case "edit":
		return editRepository(c, a[1:])
	case "delete":
		return deleteRepository(c, a[1:])
	default:
		return fmt.Errorf("unknown repo command %q", a[0])
	}
}
func editRepository(c cliConfig, args []string) error {
	if len(args) != 2 || args[1] != "visibility" {
		return fmt.Errorf("usage: tpahub edit <repo-url> visibility")
	}
	target, repository, err := repositoryTarget(c, args[0])
	if err != nil {
		return err
	}
	var metadata struct {
		ID, Name, Username, Visibility string
	}
	if _, err = getRepositoryMetadata(target, repository, &metadata); err != nil {
		return err
	}
	if metadata.Visibility != "private" && metadata.Visibility != "public" {
		return fmt.Errorf("invalid current repository visibility")
	}
	fmt.Printf("Repository: %s\\nCurrent visibility: %s\\n", canonicalRepositoryURL(target.Server, metadata.Username, metadata.Name), metadata.Visibility)
	next := strings.ToLower(strings.TrimSpace(repositoryPrompt("New visibility (private/public): ")))
	if next != "private" && next != "public" {
		return fmt.Errorf("visibility must be private or public")
	}
	if next == metadata.Visibility {
		return fmt.Errorf("visibility is already %s", next)
	}
	if next == "public" {
		fmt.Println("This repository will become publicly readable.")
	}
	if strings.ToLower(strings.TrimSpace(repositoryPrompt("Change repository visibility? [y/N] "))) != "y" {
		fmt.Println("Visibility change cancelled.")
		return nil
	}
	body, _ := json.Marshal(map[string]string{"visibility": next})
	req, err := http.NewRequest(http.MethodPatch, target.Server+"/v1/repositories/"+url.PathEscape(repository), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+target.Token)
	res, err := doCLIRequest(req)
	if err != nil {
		return err
	}
	return printResponse(res)
}

func deleteRepository(c cliConfig, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: tpahub delete <repo-url>")
	}
	target, repository, err := repositoryTarget(c, args[0])
	if err != nil {
		return err
	}
	var metadata struct {
		ID, Name, Username, Visibility string
	}
	if _, err = getRepositoryMetadata(target, repository, &metadata); err != nil {
		return err
	}
	fmt.Printf("Repository:\\n  Name: %s\\n  URL: %s\\n  Visibility: %s\\n", metadata.Name, canonicalRepositoryURL(target.Server, metadata.Username, metadata.Name), metadata.Visibility)
	if strings.ToLower(strings.TrimSpace(repositoryPrompt("Delete this repository permanently? [y/N] "))) != "y" {
		fmt.Println("Deletion cancelled.")
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, target.Server+"/v1/repositories/"+url.PathEscape(repository), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+target.Token)
	res, err := doCLIRequest(req)
	if err != nil {
		return err
	}
	if res.StatusCode == http.StatusNoContent {
		res.Body.Close()
		fmt.Println("Repository deleted.")
		return nil
	}
	return printResponse(res)
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
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
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

type RepoRef struct {
	Server   string
	ID       string
	Username string
	Name     string
}

// ParseRepoURL is the single URL-first repository identity parser. Canonical
// references use /r/<username>/<repository-name>. A one-segment UUID remains
// accepted internally for API/storage paths, but is not a user-facing form.
func ParseRepoURL(value string) (RepoRef, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return RepoRef{}, fmt.Errorf("invalid repository URL %q; use https://HOST/REPOSITORY_ID", value)
	}
	path := strings.Trim(strings.TrimSuffix(u.Path, "/"), "/")
	if path == "" || strings.Contains(path, "//") {
		return RepoRef{}, fmt.Errorf("invalid repository URL %q; use https://HOST/r/USERNAME/REPOSITORY", value)
	}
	parts := strings.Split(path, "/")
	var id, username, name string
	switch {
	case len(parts) == 3 && parts[0] == "r":
		username, name = parts[1], parts[2]
		if !validUsername(username) || !validRepositoryName(name) {
			return RepoRef{}, fmt.Errorf("invalid repository URL %q; use safe lowercase repository components", value)
		}
	case len(parts) == 1 && repositoryID(parts[0]):
		id = parts[0]
	default:
		return RepoRef{}, fmt.Errorf("invalid repository URL %q; use https://HOST/r/USERNAME/REPOSITORY", value)
	}
	originURL := *u
	originURL.Path = ""
	originURL.RawPath = ""
	origin, err := webOrigin(&originURL)
	if err != nil {
		return RepoRef{}, fmt.Errorf("invalid repository URL %q: %w", value, err)
	}
	return RepoRef{Server: origin, ID: id, Username: username, Name: name}, nil
}

func parseRepositoryReference(value string) (string, string, error) {
	ref, err := ParseRepoURL(value)
	if err != nil {
		return "", "", err
	}
	return ref.Server, ref.ID, nil
}

func repositoryTarget(c cliConfig, value string) (cliConfig, string, error) {
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		ref, err := ParseRepoURL(value)
		if err != nil {
			return c, "", err
		}
		target := c.selectServer(ref.Server)
		if ref.ID != "" {
			return target, ref.ID, nil
		}
		id, err := resolveCanonicalRepository(target, ref.Username, ref.Name)
		return target, id, err
	}
	id, err := resolveRepository(c, value)
	return c, id, err
}

func resolveCanonicalRepository(c cliConfig, username, name string) (string, error) {
	type repository struct {
		ID, Name, Username string
	}
	var repositories []repository
	if c.Token != "" {
		if err := getJSON(c.Server+"/v1/repositories", c.Token, &repositories); err != nil {
			return "", err
		}
	}
	var public []repository
	if err := getJSON(c.Server+"/v1/public/repositories", "", &public); err != nil {
		return "", err
	}
	repositories = append(repositories, public...)
	match := ""
	for _, repository := range repositories {
		if repository.Username == username && repository.Name == name {
			if !repositoryID(repository.ID) {
				return "", fmt.Errorf("server returned an invalid repository ID")
			}
			if match != "" && match != repository.ID {
				return "", fmt.Errorf("repository %q is ambiguous", username+"/"+name)
			}
			match = repository.ID
		}
	}
	if match == "" {
		return "", fmt.Errorf("repository %q not found", username+"/"+name)
	}
	return match, nil
}

func repositoryID(value string) bool {
	if len(value) == 32 {
		valid := true
		for _, character := range value {
			if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
				valid = false
				break
			}
		}
		return valid
	}
	return false
}
func resolveRepository(c cliConfig, value string) (string, error) {
	return resolveRepositoryInScope(c, value, false)
}

// Mutation names are account scoped. Read/discovery names are drawn from the
// union of the authenticated account's repositories and public repositories.
// Every subsequent operation uses the resolved immutable ID, not the name.
func resolveRepositoryInScope(c cliConfig, value string, ownerOnly bool) (string, error) {
	if repositoryID(value) {
		return value, nil
	}
	type repository struct{ ID, Name, Visibility string }
	var repositories []repository
	if ownerOnly || c.Token != "" {
		if c.Token == "" {
			return "", fmt.Errorf("login required to resolve an owned repository name")
		}
		if err := getJSON(c.Server+"/v1/repositories", c.Token, &repositories); err != nil {
			return "", fmt.Errorf("discover owned repositories: %w", err)
		}
	}
	if !ownerOnly {
		var public []repository
		if err := getJSON(c.Server+"/v1/public/repositories", "", &public); err != nil {
			return "", fmt.Errorf("discover public repositories: %w", err)
		}
		repositories = append(repositories, public...)
	}
	match := ""
	for _, repository := range repositories {
		if repository.Name != value {
			continue
		}
		if !repositoryID(repository.ID) {
			return "", fmt.Errorf("server returned an invalid repository ID")
		}
		if match != "" && match != repository.ID {
			return "", fmt.Errorf("repository name %q is ambiguous; use its repository ID", value)
		}
		match = repository.ID
	}
	if match == "" {
		return "", fmt.Errorf("repository %q not found in the accessible scope; private names require owner login, otherwise use the repository ID", value)
	}
	return match, nil
}

func validateReaderCredential(c cliConfig, repo, reader string) error {
	req, err := http.NewRequest("GET", c.Server+"/r/"+url.PathEscape(repo)+"/dists/stable/InRelease", nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth("reader", reader)
	res, err := doCLIRequest(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, res.Body)
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return fmt.Errorf("repository password rejected")
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("repository access check failed: %s", res.Status)
	}
	return nil
}

func getRepositoryMetadata(c cliConfig, repo string, out any) (int, error) {
	req, err := http.NewRequest("GET", c.Server+"/v1/repositories/"+url.PathEscape(repo), nil)
	if err != nil {
		return 0, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := doCLIRequest(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("request failed: %s", res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return res.StatusCode, err
	}
	return res.StatusCode, nil
}

var repositoryPasswordReader = readRepositoryPassword

func addRepo(c cliConfig, v, reader string, dryRun bool) error {
	// The repository command resolved a canonical reference or internal ID. Reader-based APT
	// registration needs no publisher credential or authenticated discovery.
	// Publishing itself never needs local APT files; only installation does.
	if !dryRun && os.Geteuid() != 0 {
		return fmt.Errorf("add installs system APT configuration and requires root; rerun with sudo, or use --dry-run to inspect the files. Publishing does not require repo add")
	}
	repo := v
	var e error
	var meta struct{ ID, Visibility string }
	if reader != "" {
		meta.ID = repo
		meta.Visibility = "private"
	} else {
		status, fetchErr := getRepositoryMetadata(c, repo, &meta)
		if fetchErr != nil && status != http.StatusUnauthorized && status != http.StatusForbidden {
			return fetchErr
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			meta.ID = repo
			meta.Visibility = "private"
		}
	}
	if meta.Visibility == "private" && reader == "" {
		reader, e = repositoryPasswordReader()
		if e != nil {
			return e
		}
		if reader == "" {
			return fmt.Errorf("repository password cannot be empty")
		}
	}
	if strings.ContainsAny(reader, " \t\r\n\"\\\x00") {
		return fmt.Errorf("invalid reader credential")
	}
	if meta.Visibility == "private" {
		if e = validateReaderCredential(c, repo, reader); e != nil {
			return e
		}
	}
	req, e := http.NewRequest("GET", c.Server+"/v1/repositories/"+repo+"/signing-key", nil)
	if e != nil {
		return e
	}
	key, e := doCLIRequest(req)
	if e != nil {
		return e
	}
	defer key.Body.Close()
	if key.StatusCode != http.StatusOK {
		return fmt.Errorf("signing key download failed: %s", key.Status)
	}
	kb, e := io.ReadAll(key.Body)
	if e != nil {
		return e
	}
	name := "tpahub-" + repo
	keyPath := "/etc/apt/keyrings/" + name + ".gpg"
	src := "/etc/apt/sources.list.d/" + name + ".list"
	line := fmt.Sprintf("# managed-by: tpahub\n# repository-id: %s\ndeb [signed-by=%s] %s/r/%s/ stable main\n", repo, keyPath, c.Server, repo)
	auth := ""
	if meta.Visibility == "private" {
		if reader == "" {
			return fmt.Errorf("private repository requires --token")
		}
		if strings.ContainsAny(reader, " \t\r\n") {
			return fmt.Errorf("invalid reader credential")
		}
		auth = "machine " + strings.TrimRight(c.Server, "/") + "/r/" + repo + "/ login reader password " + reader + "\n"
	}
	if dryRun {
		fmt.Printf("Would install %s (0644)\n", keyPath)
		fmt.Printf("Would install %s (0644): %s", src, line)
		if auth != "" {
			fmt.Printf("Would install /etc/apt/auth.conf.d/%s (0600)\n", name)
		}
		return nil
	}
	if e = os.MkdirAll("/etc/apt/keyrings", 0755); e != nil {
		return e
	}
	if e = writeAPTFile(keyPath, kb, 0644); e != nil {
		return e
	}
	if auth != "" {
		if e = os.MkdirAll("/etc/apt/auth.conf.d", 0755); e != nil {
			return e
		}
		if e = writeAPTFile("/etc/apt/auth.conf.d/"+name, []byte(auth), 0600); e != nil {
			return e
		}
	}
	if e = os.MkdirAll(filepath.Dir(src), 0755); e != nil {
		return e
	}
	if e = writeAPTFile(src, []byte(line), 0644); e != nil {
		return e
	}
	// A repository may have been private before. Do not leave a revoked or
	// unnecessary reader credential behind when it is re-added as public.
	if auth == "" {
		_ = os.Remove("/etc/apt/auth.conf.d/" + name)
	}
	return nil
}

// Replace credential files atomically, even when an old file has unsafe modes.
func writeAPTFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tpahub-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func removeRepo(c cliConfig, v string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("remove changes system APT configuration and requires root; rerun with sudo")
	}
	repo, e := resolveRepository(c, v)
	if e != nil {
		return e
	}
	entries, err := discoverInstalledRepositories(aptRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Managed && entry.RepositoryID == repo {
			if err := os.Remove(entry.SourceFile); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	name := "tpahub-" + repo
	for _, p := range []string{filepath.Join(aptRoot, "keyrings", name+".gpg"), filepath.Join(aptRoot, "auth.conf.d", name)} {
		_ = os.Remove(p)
	}
	return nil
}
