package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh/terminal"
)

var publishPrompt = func(prompt string) string { return input(prompt, false) }
var publishInteractive = func() bool { return terminal.IsTerminal(int(os.Stdin.Fd())) }

func stagedPlanInputs(manifest stagingManifest) ([]publicationPlanInput, error) {
	inputs := make([]publicationPlanInput, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if err := verifyStagedArtifact(artifact); err != nil {
			return nil, err
		}
		inputs = append(inputs, publicationPlanInput{Filename: filepath.Base(artifact.StagedPath), Package: artifact.Package, Version: artifact.Version, Architecture: artifact.Architecture, Size: artifact.Size, SHA256: artifact.SHA256})
	}
	return inputs, nil
}

func postJSONAuth(u, token string, value any, out any) (int, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := doCLIRequest(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	data, readErr := io.ReadAll(res.Body)
	if readErr != nil {
		return res.StatusCode, readErr
	}
	if err := json.Unmarshal(data, out); err != nil {
		return res.StatusCode, fmt.Errorf("decode request response (%s): %s", res.Status, redactSecrets(strings.TrimSpace(string(data))))
	}
	return res.StatusCode, nil
}

func printPublicationPlan(plan publicationPlanResponse, verbose bool) {
	fmt.Print(publicationPlanOutput(plan, verbose))
}

func publicationPlanOutput(plan publicationPlanResponse, verbose bool) string {
	var out strings.Builder
	if verbose {
		fmt.Fprint(&out, "Staged packages\n\n")
		for _, item := range plan.Items {
			fmt.Fprintf(&out, "%-14s %-24s %-12s %-10s %s\n", item.Classification, item.Package, item.Version, item.Architecture, formatBytes(item.Size))
		}
		fmt.Fprintf(&out, "\nCurrent repository\n\n%d packages\n", plan.CurrentPackageCount)
		if plan.StorageLimitBytes == math.MaxInt64 {
			fmt.Fprintf(&out, "%s\n", formatBytes(plan.CurrentStorageBytes))
		} else {
			fmt.Fprintf(&out, "%s / %s\n", formatBytes(plan.CurrentStorageBytes), formatBytes(plan.StorageLimitBytes))
		}
		out.WriteString("\n")
	}
	storageDelta := plan.ProjectedStorageBytes - plan.CurrentStorageBytes
	out.WriteString("New repository statistics\n\n")
	fmt.Fprintf(&out, "%d packages (+%d)\n", plan.ProjectedPackageCount, plan.NewPackageCount)
	if plan.StorageLimitBytes == math.MaxInt64 {
		fmt.Fprintf(&out, "%s (%s)\n", formatBytes(plan.ProjectedStorageBytes), formatDeltaBytes(storageDelta))
	} else {
		fmt.Fprintf(&out, "%s / %s (%s)\n", formatBytes(plan.ProjectedStorageBytes), formatBytes(plan.StorageLimitBytes), formatDeltaBytes(storageDelta))
	}
	return out.String()
}

func formatDeltaBytes(bytes int64) string {
	if bytes < 0 {
		return "-" + formatBytes(-bytes)
	}
	return "+" + formatBytes(bytes)
}

func publishStaged(c cliConfig, repository string, yes, verbose bool, noWaitOption ...bool) error {
	noWait := len(noWaitOption) > 0 && noWaitOption[0]
	target, repo, err := repositoryTarget(c, repository)
	if err != nil {
		return err
	}
	return withStagingLock(func() error {
		manifest, err := loadStagingManifest()
		if err != nil {
			return err
		}
		if len(manifest.Artifacts) == 0 {
			return fmt.Errorf("Nothing staged for publication. Use `tparun stage <package.deb>...` first.")
		}
		fmt.Printf("Preparing publication: %d files\n", len(manifest.Artifacts))
		inputs, err := stagedPlanInputs(manifest)
		if err != nil {
			return err
		}
		var start publicationSessionStart
		status, err := postJSONAuth(target.Server+"/v1/repositories/"+repo+"/publication-sessions", target.Token, inputs, &start)
		if err != nil {
			return err
		}
		printPublicationPlan(start.Plan, verbose)
		if status >= 300 || !start.Plan.Allowed {
			if start.Plan.StorageLimitBytes != math.MaxInt64 && start.Plan.ProjectedAccountStorageBytes > start.Plan.StorageLimitBytes {
				excess := start.Plan.ProjectedAccountStorageBytes - start.Plan.StorageLimitBytes
				return fmt.Errorf("publication exceeds available storage by %s", formatBytes(excess))
			}
			if start.Plan.Reason != "" {
				return fmt.Errorf("publication preflight rejected: %s", start.Plan.Reason)
			}
			return fmt.Errorf("publication preflight rejected")
		}
		finalized := false
		if start.SessionID != "" {
			defer func() {
				if !finalized {
					abortPublicationSession(target.Server, target.Token, start.SessionID)
				}
			}()
		}
		if start.SessionID == "" || start.BatchSize < 1 || start.BatchSize > maxUploadFiles {
			return fmt.Errorf("server returned invalid publication session details")
		}
		if !yes {
			if !publishInteractive() {
				return fmt.Errorf("publication requires confirmation; use --yes for non-interactive execution")
			}
			answer := strings.ToLower(strings.TrimSpace(publishPrompt("Publish? [y/N] ")))
			if answer != "y" && answer != "yes" {
				fmt.Println("publication cancelled; staging preserved")
				return nil
			}
		}
		totalBatches := (len(manifest.Artifacts) + start.BatchSize - 1) / start.BatchSize
		for batch := 0; batch < totalBatches; batch++ {
			first := batch * start.BatchSize
			last := first + start.BatchSize
			if last > len(manifest.Artifacts) {
				last = len(manifest.Artifacts)
			}
			var body bytes.Buffer
			writer := newMultipart(&body)
			for _, artifact := range manifest.Artifacts[first:last] {
				if err := writer.AddFile("files", artifact.StagedPath); err != nil {
					return fmt.Errorf("prepare publication batch %d/%d: %w", batch+1, totalBatches, err)
				}
			}
			if err := writer.Close(); err != nil {
				return fmt.Errorf("prepare publication batch %d/%d: %w", batch+1, totalBatches, err)
			}
			u := fmt.Sprintf("%s/v1/repositories/%s/publishes?session=%s&batch=%d", target.Server, repo, start.SessionID, batch)
			req, err := http.NewRequest(http.MethodPost, u, &body)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+target.Token)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			fmt.Printf("Uploading batch %d/%d ... ", batch+1, totalBatches)
			res, err := doCLIRequest(req)
			if err != nil {
				fmt.Println("failed")
				return fmt.Errorf("publication batch %d/%d upload failed: %w", batch+1, totalBatches, err)
			}
			data, readErr := io.ReadAll(res.Body)
			res.Body.Close()
			if readErr != nil {
				fmt.Println("failed")
				return fmt.Errorf("publication batch %d/%d response failed: %w", batch+1, totalBatches, readErr)
			}
			if res.StatusCode >= 300 {
				fmt.Println("failed")
				return fmt.Errorf("publication batch %d/%d upload failed: %s: %s", batch+1, totalBatches, res.Status, strings.TrimSpace(string(data)))
			}
			var accepted struct {
				SessionID string `json:"session_id"`
				Batch     int    `json:"batch"`
				State     string `json:"state"`
			}
			if err := json.Unmarshal(data, &accepted); err != nil || accepted.SessionID != start.SessionID || accepted.Batch != batch || accepted.State != "accepted" {
				fmt.Println("failed")
				return fmt.Errorf("publication batch %d/%d was not confirmed", batch+1, totalBatches)
			}
			fmt.Println("accepted")
		}
		fmt.Println("Finalizing publication...")
		finalizeURL := target.Server + "/v1/publication-sessions/" + start.SessionID + "/finalize"
		finalizeRequest, err := http.NewRequest(http.MethodPost, finalizeURL, nil)
		if err != nil {
			return err
		}
		finalizeRequest.Header.Set("Authorization", "Bearer "+target.Token)
		finalizeResponse, err := doCLIRequest(finalizeRequest)
		if err != nil {
			return fmt.Errorf("publication finalization failed: %w", err)
		}
		finalizeData, readErr := io.ReadAll(finalizeResponse.Body)
		finalizeResponse.Body.Close()
		if readErr != nil {
			return fmt.Errorf("publication finalization response failed: %w", readErr)
		}
		if finalizeResponse.StatusCode >= 300 {
			return fmt.Errorf("publication finalization failed: %s: %s", finalizeResponse.Status, strings.TrimSpace(string(finalizeData)))
		}
		var accepted struct{ ID, State string }
		if err := json.Unmarshal(finalizeData, &accepted); err != nil || accepted.ID != start.SessionID || accepted.State != "queued" {
			return fmt.Errorf("publication finalization was not confirmed")
		}
		finalized = true
		if noWait {
			fmt.Printf("Publication queued: %s\n", accepted.ID)
			return nil
		}
		fmt.Println("\nPublishing...")
		state, jobErr := waitForPublication(target.Server, target.Token, accepted.ID)
		if jobErr != nil {
			return jobErr
		}
		if state != "succeeded" {
			return fmt.Errorf("publication ended in state %s; staging preserved", state)
		}
		if err := clearStagingLocked(manifest); err != nil {
			return err
		}
		fmt.Printf("%d packages published. Staging cleared.\n", len(manifest.Artifacts))
		return nil
	})
}

func abortPublicationSession(server, token, sessionID string) {
	request, err := http.NewRequest(http.MethodDelete, server+"/v1/publication-sessions/"+sessionID, nil)
	if err != nil {
		return
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := doCLIRequest(request)
	if err == nil {
		response.Body.Close()
	}
}

func waitForPublication(server, token, jobID string) (string, error) {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		var job struct {
			State string `json:"state"`
			Error string `json:"error"`
		}
		if err := getJSON(server+"/v1/publish-jobs/"+jobID, token, &job); err != nil {
			return "", err
		}
		if job.State == "succeeded" {
			return job.State, nil
		}
		if job.State == "failed" {
			if job.Error != "" {
				return job.State, fmt.Errorf("publication failed: %s; staging preserved", job.Error)
			}
			return job.State, fmt.Errorf("publication failed; staging preserved")
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("publication timed out; job %s remains available and staging was preserved", jobID)
		}
		timer := time.NewTimer(time.Second)
		<-timer.C
	}
}
