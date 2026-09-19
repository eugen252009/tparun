package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
		inputs = append(inputs, publicationPlanInput{Filename: artifact.Filename, Package: artifact.Package, Version: artifact.Version, Architecture: artifact.Architecture, Size: artifact.Size, SHA256: artifact.SHA256})
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
		return res.StatusCode, fmt.Errorf("decode request response (%s): %s", res.Status, strings.TrimSpace(string(data)))
	}
	return res.StatusCode, nil
}

func printPublicationPlan(repository string, plan publicationPlanResponse) {
	fmt.Printf("Repository:\n  %s\n\nCurrent:\n  %d packages\n  %s used\n\nStaged:\n", repository, plan.CurrentPackageCount, formatBytes(plan.CurrentStorageBytes))
	for _, item := range plan.Items {
		fmt.Printf("  %s %s %s  %s\n", item.Classification, item.Package, item.Version, item.Architecture)
	}
	fmt.Printf("  %d packages\n  %s\n\nAfter publication:\n  %d packages\n  %s used / %s limit\n  %s remaining\n", plan.StagedPackageCount, formatBytes(plan.StagedStorageBytes), plan.ProjectedPackageCount, formatBytes(plan.ProjectedAccountStorageBytes), formatBytes(plan.StorageLimitBytes), formatBytes(plan.RemainingStorageBytes))
}

func publishStaged(c cliConfig, repository string, yes bool) error {
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
			return fmt.Errorf("nothing staged for publication; use `tpahub stage <package.deb>...` first")
		}
		inputs, err := stagedPlanInputs(manifest)
		if err != nil {
			return err
		}
		var plan publicationPlanResponse
		status, err := postJSONAuth(target.Server+"/v1/repositories/"+repo+"/publication-plan", target.Token, inputs, &plan)
		if err != nil {
			return err
		}
		printPublicationPlan(target.Server+"/"+repo, plan)
		if status >= 300 || !plan.Allowed {
			if plan.Reason != "" {
				return fmt.Errorf("publication preflight rejected: %s", plan.Reason)
			}
			return fmt.Errorf("publication preflight rejected")
		}
		if !yes {
			if !publishInteractive() {
				return fmt.Errorf("publication requires confirmation; use --yes for non-interactive execution")
			}
			answer := strings.ToLower(strings.TrimSpace(publishPrompt("Publish these packages? [y/N] ")))
			if answer != "y" && answer != "yes" {
				fmt.Println("Publication cancelled; staging preserved.")
				return nil
			}
		}
		for _, artifact := range manifest.Artifacts {
			if err := verifyStagedArtifact(artifact); err != nil {
				return err
			}
		}
		var body bytes.Buffer
		writer := newMultipart(&body)
		for _, artifact := range manifest.Artifacts {
			if err := writer.AddFile("files", artifact.StagedPath); err != nil {
				return err
			}
		}
		writer.Close()
		req, err := http.NewRequest(http.MethodPost, target.Server+"/v1/repositories/"+repo+"/publishes", &body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+target.Token)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		res, err := doCLIRequest(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode >= 300 {
			data, _ := io.ReadAll(res.Body)
			return fmt.Errorf("publication upload failed: %s: %s", res.Status, strings.TrimSpace(string(data)))
		}
		var accepted struct{ ID, State string }
		if err := json.NewDecoder(res.Body).Decode(&accepted); err != nil {
			return err
		}
		if accepted.ID == "" {
			return fmt.Errorf("publication response did not include a job ID")
		}
		fmt.Printf("\nPublication accepted. Job: %s\n", accepted.ID)
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
		time.Sleep(time.Second)
	}
}
