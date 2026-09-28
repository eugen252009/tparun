package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/eugen252009/tpahub/internal/tpa"
)

const operatorPublicationStatusPath = "/ops/api/publication-status"

type publicationOperationalStatus struct {
	QueuedJobs         int64 `json:"queued_jobs"`
	RunningJobs        int64 `json:"running_jobs"`
	ActiveAttempts     int64 `json:"active_attempts"`
	IncompleteSessions int64 `json:"incomplete_sessions"`
	ExecutionIdle      bool  `json:"execution_idle"`
	FullyQuiescent     bool  `json:"fully_quiescent"`
}

// runPublicationAttempt marks the exact bounded runner-call lifetime. A queued
// or running job remains visible in persistent status before and after this
// process-local counter is active.
func (s *server) runPublicationAttempt(ctx context.Context, request tpa.PublishRequest, maximum, inodeLimit int64) error {
	bounded, ok := s.runner.(boundedPublicationRunner)
	if !ok {
		return fmt.Errorf("publication runner does not enforce an output bound")
	}
	s.activeAttempts.Add(1)
	defer s.activeAttempts.Add(-1)
	if boundedWithInodes, supportsInodes := s.runner.(boundedPublicationRunnerWithInodes); supportsInodes {
		return boundedWithInodes.PublishBoundedWithInodes(ctx, request, maximum, inodeLimit)
	}
	return bounded.PublishBounded(ctx, request, maximum)
}

func (s *server) publicationOperationalStatus(w http.ResponseWriter, r *http.Request) {
	operatorSecurityHeaders(w)
	if !s.operatorAuthorized(r) {
		bad(w, http.StatusUnauthorized, "operator authentication required")
		return
	}
	if r.Method != http.MethodGet {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	persisted, err := s.db.PublicationQueueStatus(r.Context())
	if err != nil {
		bad(w, http.StatusInternalServerError, "publication status unavailable")
		return
	}
	activeAttempts := s.activeAttempts.Load()
	executionIdle := persisted.QueuedJobs == 0 && persisted.RunningJobs == 0 && activeAttempts == 0
	jsonOut(w, publicationOperationalStatus{
		QueuedJobs:         persisted.QueuedJobs,
		RunningJobs:        persisted.RunningJobs,
		ActiveAttempts:     activeAttempts,
		IncompleteSessions: persisted.IncompleteSessions,
		ExecutionIdle:      executionIdle,
		FullyQuiescent:     executionIdle && persisted.IncompleteSessions == 0,
	})
}

func readOperatorSessionCookie(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("operator session cookie file required; use --cookie-file")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read operator session cookie file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 512 {
		return "", fmt.Errorf("operator session cookie file must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read operator session cookie file: %w", err)
	}
	value := strings.TrimSpace(string(data))
	if !strings.HasPrefix(value, "ops_") {
		return "", fmt.Errorf("operator session cookie file has an invalid value")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "ops_"))
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("operator session cookie file has an invalid value")
	}
	return value, nil
}

func adminPublicationStatusCLI(args []string) error {
	var cookieFile, serverOverride string
	cookieFileSet, serverSet := false, false
	jsonOutput := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--help" || args[i] == "-h":
			fmt.Println("Usage: tparun admin publication-status --cookie-file <path> [--server <origin>] [--json]")
			fmt.Println("The file contains only the current tpahub_operator_session cookie value and must not be group/world accessible.")
			return nil
		case args[i] == "--json":
			jsonOutput = true
		case args[i] == "--cookie-file" || args[i] == "--server":
			flag := args[i]
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") {
				return fmt.Errorf("%s requires a value", flag)
			}
			i++
			if flag == "--cookie-file" {
				if cookieFileSet {
					return fmt.Errorf("--cookie-file specified more than once")
				}
				cookieFile, cookieFileSet = args[i], true
			} else {
				if serverSet {
					return fmt.Errorf("--server specified more than once")
				}
				serverOverride, serverSet = args[i], true
			}
		case strings.HasPrefix(args[i], "--cookie-file="):
			if cookieFileSet {
				return fmt.Errorf("--cookie-file specified more than once")
			}
			cookieFile = strings.TrimPrefix(args[i], "--cookie-file=")
			if cookieFile == "" {
				return fmt.Errorf("--cookie-file requires a value")
			}
			cookieFileSet = true
		case strings.HasPrefix(args[i], "--server="):
			if serverSet {
				return fmt.Errorf("--server specified more than once")
			}
			serverOverride = strings.TrimPrefix(args[i], "--server=")
			if serverOverride == "" {
				return fmt.Errorf("--server requires a value")
			}
			serverSet = true
		default:
			return fmt.Errorf("unknown publication-status option %q", args[i])
		}
	}
	cookie, err := readOperatorSessionCookie(cookieFile)
	if err != nil {
		return err
	}
	cli := loadCLIForServer(serverOverride)
	if cli.err != nil {
		return cli.err
	}
	req, err := http.NewRequest(http.MethodGet, cli.Server+operatorPublicationStatusPath, nil)
	if err != nil {
		return err
	}
	req.AddCookie(&http.Cookie{Name: operatorSessionCookie, Value: cookie})
	res, err := doCLIRequest(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("publication status request failed: %s", res.Status)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	var status publicationOperationalStatus
	if err = json.Unmarshal(body, &status); err != nil {
		return fmt.Errorf("invalid publication status response")
	}
	if err = status.validate(); err != nil {
		return err
	}
	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	fmt.Printf("Queued jobs: %d\nRunning jobs: %d\nActive attempts: %d\nIncomplete sessions: %d\nExecution idle: %s\nFully quiescent: %s\n",
		status.QueuedJobs, status.RunningJobs, status.ActiveAttempts, status.IncompleteSessions,
		yesNo(status.ExecutionIdle), yesNo(status.FullyQuiescent))
	return nil
}

func (status publicationOperationalStatus) validate() error {
	if status.QueuedJobs < 0 || status.RunningJobs < 0 || status.ActiveAttempts < 0 || status.IncompleteSessions < 0 {
		return fmt.Errorf("invalid publication status response")
	}
	executionIdle := status.QueuedJobs == 0 && status.RunningJobs == 0 && status.ActiveAttempts == 0
	if status.ExecutionIdle != executionIdle || status.FullyQuiescent != (executionIdle && status.IncompleteSessions == 0) {
		return fmt.Errorf("inconsistent publication status response")
	}
	return nil
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
