package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var aptRoot = "/etc/apt"

type installedRepository struct {
	Origin       string
	RepositoryID string
	SourceFile   string
	Managed      bool
	Format       string
	Legacy       bool
	Display      string
}

func installedCLI(args []string) error {
	verbose := has(args, "--verbose")
	for _, arg := range args {
		if arg != "--verbose" {
			return fmt.Errorf("usage: tpahub installed [--verbose]")
		}
	}
	entries, err := discoverInstalledRepositories(aptRoot)
	if err != nil {
		return err
	}
	for i := range entries {
		entries[i].Display = installedDisplay(entries[i])
		if verbose {
			fmt.Println(entries[i].Display)
			fmt.Printf("  %s\n", entries[i].SourceFile)
			if entries[i].Managed {
				fmt.Println("  managed by tpahub")
			} else {
				fmt.Println("  unmanaged source")
			}
			if entries[i].RepositoryID != "" {
				fmt.Printf("  repository id: %s\n", entries[i].RepositoryID)
			}
			if entries[i].Legacy {
				fmt.Println("  legacy origin")
			}
			fmt.Printf("  apt format: %s\n", entries[i].Format)
		} else {
			fmt.Println(entries[i].Display)
		}
	}
	return nil
}

func discoverInstalledRepositories(root string) ([]installedRepository, error) {
	files := []string{filepath.Join(root, "sources.list")}
	for _, dir := range []string{filepath.Join(root, "sources.list.d")} {
		matches, err := filepath.Glob(filepath.Join(dir, "*"))
		if err != nil {
			return nil, err
		}
		files = append(files, matches...)
	}
	sort.Strings(files)
	var result []installedRepository
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if info.IsDir() {
			continue
		}
		switch filepath.Ext(path) {
		case ".list":
			entries, err := parseAPTList(path)
			if err != nil {
				return nil, err
			}
			result = append(result, entries...)
		case ".sources":
			entries, err := parseAPTSources(path)
			if err != nil {
				return nil, err
			}
			result = append(result, entries...)
		}
	}
	return result, nil
}

func parseAPTList(path string) ([]installedRepository, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	managed := strings.HasPrefix(filepath.Base(path), "tpahub-")
	marker := false
	var result []installedRepository
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "# managed-by: tpahub") {
			marker = true
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || (fields[0] != "deb" && fields[0] != "deb-src") {
			continue
		}
		idx := 1
		if strings.HasPrefix(fields[idx], "[") {
			for idx < len(fields) && !strings.HasSuffix(fields[idx], "]") {
				idx++
			}
			idx++
		}
		if idx >= len(fields) {
			continue
		}
		entry, ok := installedFromSource(fields[idx], path, "one-line", managed || marker)
		if ok {
			result = append(result, entry)
		}
	}
	return result, scanner.Err()
}

func parseAPTSources(path string) ([]installedRepository, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	managed := strings.HasPrefix(filepath.Base(path), "tpahub-")
	marker := false
	values := map[string]string{}
	var result []installedRepository
	flush := func() {
		if uri := values["uris"]; uri != "" {
			if entry, ok := installedFromSource(strings.Fields(uri)[0], path, "deb822", managed || marker); ok {
				result = append(result, entry)
			}
		}
		values = map[string]string{}
		marker = false
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "# managed-by: tpahub") {
			marker = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			values[strings.ToLower(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()
	return result, nil
}

func installedFromSource(raw, path, format string, managed bool) (installedRepository, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return installedRepository{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "r" {
		return installedRepository{}, false
	}
	id := ""
	if len(parts) == 2 && repositoryID(parts[1]) {
		id = parts[1]
	}
	return installedRepository{Origin: strings.TrimRight(u.Scheme+"://"+u.Host, "/"), RepositoryID: id, SourceFile: path, Managed: managed, Format: format, Legacy: !strings.Contains(u.Host, "tpa.run")}, true
}

func installedDisplay(entry installedRepository) string {
	if entry.RepositoryID != "" {
		if ref := lookupInstalledCanonical(entry.Origin, entry.RepositoryID); ref != "" {
			return ref
		}
	}
	if entry.RepositoryID != "" {
		return entry.Origin + "/r/" + entry.RepositoryID + legacySuffix(entry.Legacy)
	}
	return entry.Origin + legacySuffix(entry.Legacy)
}

func legacySuffix(legacy bool) string {
	if legacy {
		return "  legacy"
	}
	return ""
}

func lookupInstalledCanonical(origin, id string) string {
	var metadata struct {
		ID, Name, Username string
	}
	if _, err := getRepositoryMetadata(cliConfig{Server: origin}, id, &metadata); err != nil || metadata.Name == "" || metadata.Username == "" {
		return ""
	}
	return canonicalRepositoryURL(origin, metadata.Username, metadata.Name)
}
