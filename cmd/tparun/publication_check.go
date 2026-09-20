package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type publicationCheckResponse struct {
	ID                string `json:"ID"`
	State             string `json:"State"`
	Error             string `json:"Error"`
	VerificationState string `json:"VerificationState"`
	VerificationError string `json:"VerificationError"`
	GenerationID      string `json:"GenerationID"`
	Artifacts         []struct {
		Filename          string `json:"Filename"`
		UploadedSHA256    string `json:"UploadedSHA256"`
		UploadedSize      int64  `json:"UploadedSize"`
		PublishedSHA256   string `json:"PublishedSHA256"`
		PublishedSize     int64  `json:"PublishedSize"`
		VerificationState string `json:"VerificationState"`
	} `json:"artifacts"`
}

func checkPublication(c cliConfig, id, expectedSHA string, wait bool, timeout time.Duration, asJSON bool) error {
	if !regexp.MustCompile(`^[0-9a-fA-F]{64}$`).MatchString(expectedSHA) && expectedSHA != "" {
		return fmt.Errorf("sha256 must be 64 hexadecimal characters")
	}
	deadline := time.Now().Add(timeout)
	for {
		var status publicationCheckResponse
		if err := getJSONWithSSHReauth(c, c.Server+"/v1/publish-jobs/"+id, &status); err != nil {
			return err
		}
		if expectedSHA != "" {
			found := false
			for _, artifact := range status.Artifacts {
				if strings.EqualFold(artifact.PublishedSHA256, expectedSHA) || (status.State != "succeeded" && strings.EqualFold(artifact.UploadedSHA256, expectedSHA)) {
					found = true
				}
			}
			if !found && status.State == "succeeded" {
				return fmt.Errorf("publication %s does not contain SHA256 %s", id, strings.ToLower(expectedSHA))
			}
		}
		terminal := status.State == "succeeded" || status.State == "failed"
		if !wait || terminal {
			if asJSON {
				b, _ := json.Marshal(status)
				fmt.Println(string(b))
			} else {
				fmt.Printf("%s %s verification=%s\n", status.ID, status.State, status.VerificationState)
			}
			if status.State == "failed" {
				if status.Error != "" {
					return fmt.Errorf("publication failed: %s", status.Error)
				}
				return fmt.Errorf("publication failed")
			}
			if status.State == "succeeded" && status.VerificationState != "match" {
				return fmt.Errorf("publication succeeded without verified artifact identity")
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("publication %s timed out", id)
		}
		timer := time.NewTimer(time.Second)
		<-timer.C
	}
}
