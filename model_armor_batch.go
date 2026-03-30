package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"time"
)

type RequestPayload struct {
	Input string `json:"input"`
}

type ResponsePayload struct {
	Result string `json:"result"`
}

var allowedHosts = map[string]bool{
	"api.openai.com": true,
	"api.alinia.ai":  true,
}

func validateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "https" {
		return nil, fmt.Errorf("only https scheme allowed")
	}

	if !allowedHosts[u.Host] {
		return nil, fmt.Errorf("host not allowed: %s", u.Host)
	}

	return u, nil
}

func safeHTTPPost(ctx context.Context, targetURL string, token string, payload interface{}) (*ResponsePayload, error) {

	u, err := validateURL(targetURL)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// #nosec G704 -- URL validated via allowlist + https scheme enforcement
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	// #nosec G704 -- outbound request restricted to validated allowlisted hosts
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result ResponsePayload
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return &result, nil
}

var validTemplateID = regexp.MustCompile(`^[a-zA-Z0-9\-_]+$`)

func validateTemplateID(id string) error {
	if !validTemplateID.MatchString(id) {
		return fmt.Errorf("invalid template id")
	}
	return nil
}

func safeExecCommand(ctx context.Context, templateID string) ([]byte, error) {

	if err := validateTemplateID(templateID); err != nil {
		return nil, err
	}

	// #nosec G204 G702 -- templateID strictly validated (regex); no shell invocation
	cmd := exec.CommandContext(
		ctx,
		"gcloud",
		"beta",
		"model-armor",
		"templates",
		"describe",
		templateID,
	)

	cmd.Env = []string{}
	cmd.Dir = "/tmp"

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("command failed: %w: %s", err, string(output))
	}

	return output, nil
}

func main() {

	apiURL := os.Getenv("API_URL")
	apiToken := os.Getenv("API_TOKEN")
	templateID := os.Getenv("TEMPLATE_ID")

	if apiURL == "" || apiToken == "" || templateID == "" {
		fmt.Println("Missing required environment variables")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmdOutput, err := safeExecCommand(ctx, templateID)
	if err != nil {
		fmt.Println("Command error:", err)
		os.Exit(1)
	}

	fmt.Println("Command output:", string(cmdOutput))

	payload := RequestPayload{
		Input: "test payload",
	}

	resp, err := safeHTTPPost(ctx, apiURL, apiToken, payload)
	if err != nil {
		fmt.Println("HTTP error:", err)
		os.Exit(1)
	}

	fmt.Println("API response:", resp.Result)
}
