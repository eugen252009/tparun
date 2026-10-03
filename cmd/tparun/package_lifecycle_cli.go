package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func packageLifecycleCLI(c cliConfig, args []string) error {
	operation := args[0]
	yes := false
	operands := make([]string, 0, len(args)-1)
	for _, arg := range args[1:] {
		if arg == "--yes" || arg == "-y" {
			yes = true
			continue
		}
		operands = append(operands, arg)
	}
	if operation == "unlist" && yes {
		return fmt.Errorf("--yes is only valid with delete")
	}
	if len(operands) != 4 {
		return fmt.Errorf("usage: tparun %s <repository-url> <package> <version> <architecture> [--yes]", operation)
	}
	if operation == "delete" && !yes {
		if !publishInteractive() {
			return fmt.Errorf("permanent deletion requires confirmation; use --yes for non-interactive execution")
		}
		fmt.Printf("Permanently delete package artifact?\n  Package: %s\n  Version: %s\n  Architecture: %s\n", operands[1], operands[2], operands[3])
		answer := strings.ToLower(strings.TrimSpace(publishPrompt("Delete permanently? [y/N] ")))
		if answer != "y" && answer != "yes" {
			fmt.Println("deletion cancelled; repository was not changed")
			return nil
		}
	}
	target, repositoryID, err := repositoryTarget(c, operands[0])
	if err != nil {
		return err
	}
	input := packageLifecycleRequest{Package: operands[1], Version: operands[2], Architecture: operands[3], Confirmed: operation == "delete"}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, target.Server+"/v1/repositories/"+repositoryID+"/packages/"+operation, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+target.Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", newID())
	response, err := doCLIRequest(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("package lifecycle response could not be read: %w", err)
	}
	var result packageLifecycleResponse
	if err = json.Unmarshal(responseBody, &result); err != nil {
		if response.StatusCode >= 300 {
			return fmt.Errorf("package %s request failed: %s: %s", operation, response.Status, strings.TrimSpace(string(responseBody)))
		}
		return fmt.Errorf("package lifecycle response could not be decoded: %w", err)
	}
	if response.StatusCode >= 300 {
		message := result.Error
		if message == "" {
			message = result.Result
		}
		if message == "" {
			return fmt.Errorf("package %s request failed: %s", operation, response.Status)
		}
		return fmt.Errorf("package %s request failed: %s: %s", operation, response.Status, message)
	}
	fmt.Printf("Package lifecycle job %s: %s (%s %s %s)\n", result.ID, result.State, result.Package, result.Version, result.Architecture)
	if result.GenerationID != "" {
		fmt.Printf("Active generation: %s\n", result.GenerationID)
	}
	if result.MetadataState != "" || result.ArtifactState != "" {
		fmt.Printf("Metadata: %s; artifact: %s\n", result.MetadataState, result.ArtifactState)
	}
	if result.Error != "" {
		return fmt.Errorf("package lifecycle job %s: %s", result.ID, result.Error)
	}
	return nil
}
