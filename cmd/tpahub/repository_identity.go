package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/eugen252009/tpahub/internal/store"
)

var repositoryNameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var usernameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

func validRepositoryName(name string) bool { return repositoryNameRE.MatchString(name) }
func validUsername(name string) bool       { return usernameRE.MatchString(name) }

func validateRepositoryName(name string) error {
	if !validRepositoryName(name) {
		return fmt.Errorf("invalid repository name %q; use 1-63 lowercase ASCII letters, digits, and internal hyphens", name)
	}
	return nil
}

// accountUsername is the stable public handle for the current account model.
// Accounts predate an explicit username column, so the email local-part is
// normalized into the same deliberately boring URL grammar. The complete
// email remains private and is never exposed by this derivation.
func accountUsername(email string) string {
	local := strings.ToLower(strings.TrimSpace(strings.SplitN(email, "@", 2)[0]))
	var b strings.Builder
	lastHyphen := false
	for _, r := range local {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastHyphen = false
		} else if !lastHyphen && b.Len() > 0 {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 31 {
		name = strings.TrimRight(name[:31], "-")
	}
	if !validUsername(name) {
		return "user"
	}
	return name
}

func canonicalRepositoryPath(username, name string) string {
	return "/r/" + username + "/" + name
}

func canonicalRepositoryURL(origin, username, name string) string {
	return strings.TrimRight(origin, "/") + canonicalRepositoryPath(username, name)
}

type repositoryView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Username   string `json:"username"`
	Visibility string `json:"visibility"`
	CreatedAt  string `json:"created_at,omitempty"`
}

func (s *server) resolveRepositoryPath(ctx context.Context, username, name string) (store.Repository, error) {
	if !validUsername(username) || !validRepositoryName(name) {
		return store.Repository{}, store.ErrNotFound
	}
	repositories, err := s.db.AllRepositories(ctx)
	if err != nil {
		return store.Repository{}, err
	}
	var match store.Repository
	for _, repo := range repositories {
		account, accountErr := s.db.AccountByID(ctx, repo.AccountID)
		if accountErr == nil && accountUsername(account.Email) == username && repo.Name == name {
			if match.ID != "" && match.ID != repo.ID {
				return store.Repository{}, fmt.Errorf("repository path is ambiguous")
			}
			match = repo
		}
	}
	if match.ID == "" {
		return store.Repository{}, store.ErrNotFound
	}
	return match, nil
}

func (s *server) repositoryView(ctx context.Context, repo store.Repository) (repositoryView, error) {
	account, err := s.db.AccountByID(ctx, repo.AccountID)
	if err != nil {
		return repositoryView{}, err
	}
	return repositoryView{ID: repo.ID, Name: repo.Name, Username: accountUsername(account.Email), Visibility: repo.Visibility, CreatedAt: repo.CreatedAt}, nil
}
