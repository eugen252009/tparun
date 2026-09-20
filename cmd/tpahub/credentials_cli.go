package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

type cliCredential struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Fingerprint  string   `json:"fingerprint"`
	Label        string   `json:"label"`
	CreatedAt    string   `json:"created_at"`
	Enabled      bool     `json:"enabled"`
	Revoked      bool     `json:"revoked"`
	Capabilities []string `json:"capabilities"`
}

func credentialsCLI(c cliConfig, args []string) error {
	if c.Token == "" {
		return fmt.Errorf("authentication required; run tpahub login")
	}
	if len(args) == 2 && args[0] == "add" {
		key, err := publicKeyFromIdentity(args[1])
		if err != nil {
			return err
		}
		label := input("Label: ", false)
		_, err = postJSONAuth(c.Server+"/v1/account/ssh-credentials", c.Token, map[string]string{"public_key": key, "label": label}, &struct{}{})
		return err
	}
	if len(args) != 0 {
		return fmt.Errorf("usage: tpahub account credentials [add <identity-file>]")
	}
	var items []cliCredential
	if err := getJSON(c.Server+"/v1/account/ssh-credentials", c.Token, &items); err != nil {
		return err
	}
	fmt.Println("Credentials:")
	for i, item := range items {
		fmt.Printf("\n%d. %s\n   %s\n   %s\n", i+1, item.Label, item.Fingerprint, credentialStatus(item))
	}
	if len(items) == 0 {
		fmt.Println("\nNo SSH credentials registered.")
	}
	if os.Getenv("TPAHUB_NONINTERACTIVE") == "1" || len(items) == 0 {
		return nil
	}
	choice := input("\nSelect a credential to manage (or 0 to cancel): ", false)
	n, _ := strconv.Atoi(choice)
	if n < 1 || n > len(items) {
		return nil
	}
	item := items[n-1]
	label := input("Label ["+item.Label+"]: ", false)
	if label == "" {
		label = item.Label
	}
	values := map[string]any{"label": label, "enabled": inputBool("Enabled", item.Enabled), "list": inputBool("List", hasCapability(item, "list")), "read": inputBool("Read", hasCapability(item, "read")), "write": inputBool("Write", hasCapability(item, "write"))}
	if inputBool("Revoke", false) {
		values["action"] = "revoke"
		return patchCredential(c, item.ID, values)
	}
	return patchCredential(c, item.ID, values)
}
func credentialStatus(c cliCredential) string {
	if c.Revoked {
		return "revoked"
	}
	state := "disabled"
	if c.Enabled {
		state = "enabled"
	}
	return state + " · " + strings.Join(c.Capabilities, ", ")
}
func hasCapability(c cliCredential, name string) bool {
	for _, v := range c.Capabilities {
		if v == name {
			return true
		}
	}
	return false
}
func inputBool(name string, current bool) bool {
	value := input(fmt.Sprintf("%s [%s] (y/n): ", name, map[bool]string{true: "y", false: "n"}[current]), false)
	if value == "" {
		return current
	}
	return strings.EqualFold(value, "y") || strings.EqualFold(value, "yes")
}
func patchCredential(c cliConfig, id string, value map[string]any) error {
	b, _ := json.Marshal(value)
	req, _ := http.NewRequest(http.MethodPatch, c.Server+"/v1/account/ssh-credentials/"+id, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := doCLIRequest(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("credential update failed: %s", res.Status)
	}
	return nil
}
func publicKeyFromIdentity(path string) (string, error) {
	signers, closeFn, err := sshSigners(path)
	if err != nil {
		return "", err
	}
	defer closeFn()
	if len(signers) != 1 {
		return "", fmt.Errorf("identity file did not select exactly one key")
	}
	return string(ssh.MarshalAuthorizedKey(signers[0].PublicKey())), nil
}
