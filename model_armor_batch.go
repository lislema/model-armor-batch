package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------- CONFIG ----------------

const location = "europe-west4"

// ---------------- FLAGS ----------------

var (
	rps         = flag.Int("rps", 2, "Requests per second")
	concurrency = flag.Int("concurrency", 1, "Number of workers")
	timeout     = flag.Duration("timeout", 30*time.Second, "HTTP timeout")
)

// ---------------- SECURITY VALIDATION ----------------

// Prevent SSRF (only allow expected domain)
func validateURL(u string) error {
	if !strings.HasPrefix(u, "https://modelarmor.") {
		return fmt.Errorf("invalid URL: %s", u)
	}
	return nil
}

// Prevent command injection
func validateGcloudInput(s string) error {
	if strings.ContainsAny(s, ";&|$`") {
		return fmt.Errorf("invalid characters in input")
	}
	return nil
}

// Prevent path traversal
func validateFilePath(file string) error {
	if strings.Contains(file, "..") {
		return fmt.Errorf("invalid file path")
	}
	return nil
}

func validateTemplate(template string) error {
	if template == "" {
		return fmt.Errorf("template cannot be empty")
	}
	if strings.ContainsAny(template, " /:\\") {
		return fmt.Errorf("invalid template format")
	}
	return nil
}

// ---------------- INTERFACES ----------------

type GcloudClient interface {
	GetProject() (string, error)
	GetAccessToken() (string, error)
	ValidateTemplate(project, template string) error
}

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// ---------------- REAL GCLOUD ----------------

type realGcloud struct{}

func (g *realGcloud) GetProject() (string, error) {
	out, err := exec.Command("gcloud", "config", "get-value", "project").Output()
	if err != nil {
		return "", err
	}
	project := strings.TrimSpace(string(out))
	if project == "" {
		return "", errors.New("no active gcloud project")
	}

	if err := validateGcloudInput(project); err != nil {
		return "", err
	}

	return project, nil
}

func (g *realGcloud) GetAccessToken() (string, error) {
	out, err := exec.Command("gcloud", "auth", "print-access-token").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *realGcloud) ValidateTemplate(project, template string) error {

	if err := validateGcloudInput(template); err != nil {
		return err
	}

	cmd := exec.Command(
		"gcloud", "beta", "model-armor", "templates", "describe",
		template,
		"--project", project,
		"--location", location,
	)
	return cmd.Run()
}

// ---------------- MOCK ----------------

type mockGcloud struct{}

func (m *mockGcloud) GetProject() (string, error) { return "test", nil }
func (m *mockGcloud) GetAccessToken() (string, error) {
	return "token", nil
}
func (m *mockGcloud) ValidateTemplate(project, template string) error {
	return nil
}

// ---------------- REDACTION ----------------

var emailRegex = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,6}`)

func redact(s string) string {
	return emailRegex.ReplaceAllString(s, "[REDACTED_EMAIL]")
}

// ---------------- INPUT ----------------

func readParagraphs(file string) ([]string, error) {

	if err := validateFilePath(file); err != nil {
		return nil, err
	}

	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := f.Close(); err != nil {
			fmt.Println("error closing file:", err)
		}
	}()

	var records []string
	var buf strings.Builder

	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := scanner.Text()

		if strings.TrimSpace(line) == "" {
			if buf.Len() > 0 {
				records = append(records, strings.TrimSpace(buf.String()))
				buf.Reset()
			}
			continue
		}

		buf.WriteString(line + "\n")
	}

	if buf.Len() > 0 {
		records = append(records, strings.TrimSpace(buf.String()))
	}

	return records, scanner.Err()
}

// ---------------- METRICS ----------------

func percentile(data []int64, p float64) int64 {
	if len(data) == 0 {
		return 0
	}

	index := int(math.Ceil(p*float64(len(data)))) - 1

	if index < 0 {
		index = 0
	}
	if index >= len(data) {
		index = len(data) - 1
	}

	return data[index]
}

// ---------------- RATE LIMITER ----------------

func rateLimiter(rps int) <-chan time.Time {
	if rps <= 0 {
		rps = 1
	}
	return time.Tick(time.Second / time.Duration(rps))
}

// ---------------- WORKER ----------------

func worker(
	wg *sync.WaitGroup,
	jobs <-chan string,
	results chan<- int64,
	client HTTPClient,
	url string,
	token string,
	limiter <-chan time.Time,
) {
	defer wg.Done()

	for text := range jobs {

		start := time.Now()
		<-limiter

		payload := map[string]interface{}{
			"userPromptData": map[string]string{"text": text},
		}

		body, _ := json.Marshal(payload)

		if err := validateURL(url); err != nil {
			results <- 0
			continue
		}

		req, _ := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)

		if err == nil && resp != nil {
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				fmt.Println("copy error:", err)
			}
			if err := resp.Body.Close(); err != nil {
				fmt.Println("close error:", err)
			}
		}

		results <- time.Since(start).Milliseconds()
	}
}

// ---------------- CORE ----------------

func run(
	inputFile string,
	template string,
	localMode bool,
	client HTTPClient,
	gcloud GcloudClient,
) ([]int64, error) {

	project, err := gcloud.GetProject()
	if err != nil {
		return nil, err
	}

	token, err := gcloud.GetAccessToken()
	if err != nil {
		return nil, err
	}

	if err := validateTemplate(template); err != nil {
		return nil, err
	}

	if !localMode {
		if err := gcloud.ValidateTemplate(project, template); err != nil {
			return nil, err
		}
	}

	url := fmt.Sprintf(
		"https://modelarmor.%s.rep.googleapis.com/v1/projects/%s/locations/%s/templates/%s:sanitizeUserPrompt",
		location, project, location, template,
	)

	if err := validateURL(url); err != nil {
		return nil, err
	}

	records, err := readParagraphs(inputFile)
	if err != nil {
		return nil, err
	}

	jobs := make(chan string, len(records))
	results := make(chan int64, len(records))

	limiter := rateLimiter(*rps)

	var wg sync.WaitGroup

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go worker(&wg, jobs, results, client, url, token, limiter)
	}

	for _, r := range records {
		jobs <- r
	}
	close(jobs)

	wg.Wait()
	close(results)

	var latencies []int64
	for l := range results {
		latencies = append(latencies, l)
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	return latencies, nil
}

// ---------------- MAIN ----------------

func main() {

	flag.Parse()

	if len(flag.Args()) != 2 {
		fmt.Println("Usage: model_armor_batch [flags] <input_file> <output_jsonl>")
		os.Exit(2)
	}

	inputFile := flag.Args()[0]
	template := os.Getenv("MODEL_ARMOR_TEMPLATE")
	localMode := os.Getenv("LOCAL_MODE") == "true"

	var client HTTPClient
	var gcloud GcloudClient

	if localMode {
		client = &http.Client{Timeout: *timeout}
		gcloud = &mockGcloud{}
	} else {
		client = &http.Client{Timeout: *timeout}
		gcloud = &realGcloud{}
	}

	latencies, err := run(inputFile, template, localMode, client, gcloud)
	if err != nil {
		fmt.Println("ERROR:", err)
		os.Exit(1)
	}

	var total int64
	for _, l := range latencies {
		total += l
	}

	fmt.Println("\n===== METRICS =====")
	fmt.Printf("Requests: %d\n", len(latencies))
	fmt.Printf("Avg: %.2f ms\n", float64(total)/float64(len(latencies)))
	fmt.Printf("P50: %d\n", percentile(latencies, 0.50))
	fmt.Printf("P95: %d\n", percentile(latencies, 0.95))
	fmt.Printf("P99: %d\n", percentile(latencies, 0.99))
	fmt.Println("====================")
}
