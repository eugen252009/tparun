package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func publishClientGeneration(c cliConfig, repository, directory, manifestPath string) error {
	target, repositoryID, err := repositoryTarget(c, repository)
	if err != nil {
		return err
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read generation manifest: %w", err)
	}
	manifest, _, err := decodeGenerationManifest(manifestData, repositoryID)
	if err != nil {
		return err
	}
	if err = verifyGenerationInventory(directory, manifest); err != nil {
		return fmt.Errorf("local generation inventory verification: %w", err)
	}
	var inRelease *generationManifestFile
	for i := range manifest.Files {
		if manifest.Files[i].Path == "dists/stable/InRelease" {
			inRelease = &manifest.Files[i]
			break
		}
	}
	if inRelease == nil || inRelease.Size == 0 {
		return fmt.Errorf("generation manifest must include a non-empty dists/stable/InRelease")
	}
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	writeDone := make(chan error, 1)
	go func() {
		writeFile := func(file generationManifestFile) error {
			part, partErr := multipartWriter.CreateFormField("file:" + file.Path)
			if partErr != nil {
				return partErr
			}
			path := filepath.Join(directory, filepath.FromSlash(file.Path))
			input, partErr := os.Open(path)
			if partErr != nil {
				return partErr
			}
			info, partErr := input.Stat()
			if partErr == nil && !info.Mode().IsRegular() {
				partErr = fmt.Errorf("generation path is not a regular file: %s", file.Path)
			}
			if partErr == nil {
				_, partErr = io.Copy(part, input)
			}
			if closeErr := input.Close(); partErr == nil {
				partErr = closeErr
			}
			return partErr
		}
		manifestPart, writeErr := multipartWriter.CreateFormField("manifest")
		if writeErr == nil {
			_, writeErr = manifestPart.Write(manifestData)
		}
		// Server admission depends on authenticating this signed metadata before
		// the client streams package indexes and archives.
		if writeErr == nil {
			writeErr = writeFile(*inRelease)
		}
		for _, file := range manifest.Files {
			if writeErr != nil {
				break
			}
			if file.Path == inRelease.Path {
				continue
			}
			writeErr = writeFile(file)
		}
		if closeErr := multipartWriter.Close(); writeErr == nil {
			writeErr = closeErr
		}
		_ = writer.CloseWithError(writeErr)
		writeDone <- writeErr
	}()
	request, err := http.NewRequest(http.MethodPost, target.Server+"/v1/repositories/"+repositoryID+"/generations", reader)
	if err != nil {
		_ = reader.CloseWithError(err)
		return err
	}
	request.Header.Set("Authorization", "Bearer "+target.Token)
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	response, requestErr := doCLIRequest(request)
	if requestErr != nil {
		_ = reader.CloseWithError(requestErr)
		<-writeDone
		return requestErr
	}
	responseData, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode >= 300 {
		// The server may reject the manifest or InRelease while the request body
		// writer is still producing later multipart parts. Stop the pipe and
		// report the authoritative server rejection, not a secondary pipe error.
		_ = reader.CloseWithError(io.ErrClosedPipe)
		<-writeDone
		if readErr != nil {
			return readErr
		}
		return fmt.Errorf("generation publication failed: %s: %s", response.Status, strings.TrimSpace(string(responseData)))
	}
	writeErr := <-writeDone
	if readErr != nil {
		return readErr
	}
	if writeErr != nil {
		return fmt.Errorf("generation upload failed: %w", writeErr)
	}
	var result struct {
		ID           string `json:"id"`
		State        string `json:"state"`
		GenerationID string `json:"generation_id"`
		APTSigner    string `json:"apt_signer"`
	}
	if err = json.Unmarshal(responseData, &result); err != nil || result.GenerationID != manifest.GenerationID || result.State != "succeeded" {
		return fmt.Errorf("server did not confirm the requested generation")
	}
	fmt.Printf("Generation %s activated; APT signer %s\n", result.GenerationID, result.APTSigner)
	return nil
}

func repositorySignerCLI(c cliConfig, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: tparun signer add|list|revoke <repository> [public-key-file|fingerprint]")
	}
	action, repoArg := args[0], args[1]
	target, repositoryID, err := repositoryTarget(c, repoArg)
	if err != nil {
		return err
	}
	url := target.Server + "/v1/repositories/" + repositoryID + "/apt-signers"
	switch action {
	case "add":
		if len(args) != 3 {
			return fmt.Errorf("usage: tparun signer add <repository> <public-key-file>")
		}
		key, err := os.ReadFile(args[2])
		if err != nil || len(key) == 0 || len(key) > 64<<10 {
			return fmt.Errorf("read public OpenPGP key: invalid or unavailable file")
		}
		var response repositorySignerView
		status, err := postJSONAuth(url, target.Token, map[string]string{"public_key": string(key)}, &response)
		if err != nil {
			return err
		}
		if status >= 300 {
			return fmt.Errorf("APT signer registration failed: HTTP %d", status)
		}
		fmt.Printf("Registered APT signer %s for repository %s\n", response.Fingerprint, repositoryID)
		return nil
	case "list":
		if len(args) != 2 {
			return fmt.Errorf("usage: tparun signer list <repository>")
		}
		request, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if target.Token != "" {
			request.Header.Set("Authorization", "Bearer "+target.Token)
		}
		response, err := doCLIRequest(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		var signers []repositorySignerView
		if err = json.NewDecoder(response.Body).Decode(&signers); err != nil {
			return err
		}
		for _, signer := range signers {
			fmt.Printf("%s\n", signer.Fingerprint)
		}
		return nil
	case "revoke":
		if len(args) != 3 {
			return fmt.Errorf("usage: tparun signer revoke <repository> <fingerprint>")
		}
		request, err := http.NewRequest(http.MethodDelete, url+"/"+args[2], nil)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+target.Token)
		response, err := doCLIRequest(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode >= 300 {
			return fmt.Errorf("APT signer revocation failed: %s", response.Status)
		}
		fmt.Printf("Revoked APT signer %s for repository %s\n", args[2], repositoryID)
		return nil
	default:
		return fmt.Errorf("unknown signer action %q", action)
	}
}
