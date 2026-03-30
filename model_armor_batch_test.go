package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

//
// ---------------- HELPERS ----------------
//

func cleanupFile(t *testing.T, file string) {
	t.Helper()
	if err := os.Remove(file); err != nil {
		t.Log("cleanup failed:", err)
	}
}

//
// ---------------- MOCK HTTP ----------------
//

type testHTTP struct{}

func (m *testHTTP) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"wasSanitized": true}`)),
	}, nil
}

//
// ---------------- REDACTION ----------------
//

func TestRedact(t *testing.T) {
	input := "email test@test.com"
	out := redact(input)

	if strings.Contains(out, "test@test.com") {
		t.Fatal("email not redacted")
	}
}

//
// ---------------- PERCENTILE ----------------
//

func TestPercentile(t *testing.T) {
	data := []int64{10, 20, 30, 40, 50}

	if percentile(data, 0.5) != 30 {
		t.Fatal("P50 incorrect")
	}

	if percentile(data, 0.95) != 50 {
		t.Fatal("P95 incorrect")
	}
}

func TestPercentileEmpty(t *testing.T) {
	if percentile([]int64{}, 0.5) != 0 {
		t.Fatal("empty percentile should be 0")
	}
}

//
// ---------------- INPUT PARSER ----------------
//

func TestReadParagraphs(t *testing.T) {
	content := "one\n\n two\nline\n\n"
	file := "test_input.txt"

	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	defer cleanupFile(t, file)

	records, err := readParagraphs(file)
	if err != nil {
		t.Fatal(err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
}

//
// ---------------- RATE LIMITER ----------------
//

func TestRateLimiter(t *testing.T) {
	limiter := rateLimiter(2)

	start := time.Now()
	<-limiter
	<-limiter

	elapsed := time.Since(start)

	if elapsed < 400*time.Millisecond {
		t.Fatal("rate limiter too fast")
	}
}

//
// ---------------- WORKER ----------------
//

func TestWorkerProcessesJobs(t *testing.T) {

	jobs := make(chan string, 3)
	results := make(chan int64, 3)

	client := &testHTTP{}

	jobs <- "a"
	jobs <- "b"
	jobs <- "c"
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(1)

	go worker(&wg, jobs, results, client, "http://test", "token", time.Tick(time.Millisecond))

	wg.Wait()
	close(results)

	count := 0
	for range results {
		count++
	}

	if count != 3 {
		t.Fatalf("expected 3 results, got %d", count)
	}
}

//
// ---------------- EDGE CASES ----------------
//

func TestEmptyInput(t *testing.T) {
	if redact("") != "" {
		t.Fatal("empty input should remain empty")
	}
}

func TestLargeInput(t *testing.T) {
	input := strings.Repeat("A", 10000)
	out := redact(input)

	if len(out) != len(input) {
		t.Fatal("unexpected modification of large input")
	}
}

//
// ---------------- RUN() ----------------
//

func TestRunLocalMode(t *testing.T) {

	content := "test prompt 1\n\ntest prompt 2\n"
	file := "test_run.txt"

	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	defer cleanupFile(t, file)

	client := &testHTTP{}
	gcloud := &mockGcloud{}

	latencies, err := run(file, "test-template", true, client, gcloud)
	if err != nil {
		t.Fatal(err)
	}

	if len(latencies) != 2 {
		t.Fatalf("expected 2 results, got %d", len(latencies))
	}

	for _, l := range latencies {
		if l <= 0 {
			t.Fatal("latency should be > 0")
		}
	}
}

//
// ---------------- HTTP ERROR ----------------
//

type failingHTTP struct{}

func (f *failingHTTP) Do(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("network error")
}

func TestRunHTTPError(t *testing.T) {

	file := "test_http.txt"

	if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	defer cleanupFile(t, file)

	client := &failingHTTP{}
	gcloud := &mockGcloud{}

	latencies, err := run(file, "t", true, client, gcloud)

	if err != nil {
		t.Fatal(err)
	}

	if len(latencies) != 1 {
		t.Fatal("expected 1 result even on error")
	}
}

//
// ---------------- NIL RESPONSE ----------------
//

type nilHTTP struct{}

func (n *nilHTTP) Do(req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestWorkerNilResponse(t *testing.T) {

	jobs := make(chan string, 1)
	results := make(chan int64, 1)

	jobs <- "test"
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(1)

	go worker(&wg, jobs, results, &nilHTTP{}, "url", "token", time.Tick(time.Millisecond))

	wg.Wait()
	close(results)

	if len(results) != 1 {
		t.Fatal("expected result")
	}
}

//
// ---------------- GCP MODE ----------------
//

func TestRunGCPMode(t *testing.T) {

	file := "test_gcp.txt"

	if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	defer cleanupFile(t, file)

	client := &testHTTP{}
	gcloud := &mockGcloud{}

	latencies, err := run(file, "template", false, client, gcloud)
	if err != nil {
		t.Fatal(err)
	}

	if len(latencies) != 1 {
		t.Fatal("expected 1 result")
	}
}

func TestValidateGcloudInput(t *testing.T) {

	if err := validateGcloudInput("valid-input"); err != nil {
		t.Fatal("valid input rejected")
	}

	if err := validateGcloudInput("bad;rm -rf"); err == nil {
		t.Fatal("dangerous input accepted")
	}
}

func TestValidateGcloudInputEdge(t *testing.T) {
	if err := validateGcloudInput("abc|123"); err == nil {
		t.Fatal("expected rejection")
	}
}

//
// ---------------- CONCURRENCY ----------------
//

func TestRunConcurrency(t *testing.T) {

	file := "test_conc.txt"

	if err := os.WriteFile(file, []byte("a\n\nb\n\nc"), 0644); err != nil {
		t.Fatal(err)
	}
	defer cleanupFile(t, file)

	*rps = 10
	*concurrency = 3

	client := &testHTTP{}
	gcloud := &mockGcloud{}

	latencies, err := run(file, "t", true, client, gcloud)
	if err != nil {
		t.Fatal(err)
	}

	if len(latencies) != 3 {
		t.Fatal("expected 3 results")
	}
}

//
// ---------------- RATE LIMITER EDGE ----------------
//

func TestRateLimiterZero(t *testing.T) {
	ch := rateLimiter(0)

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("rate limiter blocked")
	}
}

//
// ---------------- PERCENTILE BOUNDS ----------------
//

func TestPercentileBounds(t *testing.T) {
	data := []int64{10, 20, 30}

	if percentile(data, 0) != 10 {
		t.Fatal("p0 incorrect")
	}

	if percentile(data, 1) != 30 {
		t.Fatal("p100 incorrect")
	}
}

//
// ---------------- GCLOUD ERRORS ----------------
//

type badTokenGcloud struct{}

func (b *badTokenGcloud) GetProject() (string, error) { return "p", nil }
func (b *badTokenGcloud) GetAccessToken() (string, error) {
	return "", fmt.Errorf("token error")
}
func (b *badTokenGcloud) ValidateTemplate(string, string) error { return nil }

func TestRunTokenError(t *testing.T) {

	file := "test_token.txt"

	if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	defer cleanupFile(t, file)

	_, err := run(file, "t", true, &testHTTP{}, &badTokenGcloud{})

	if err == nil {
		t.Fatal("expected token error")
	}
}

type badTemplateGcloud struct{}

func (b *badTemplateGcloud) GetProject() (string, error)     { return "p", nil }
func (b *badTemplateGcloud) GetAccessToken() (string, error) { return "t", nil }
func (b *badTemplateGcloud) ValidateTemplate(string, string) error {
	return fmt.Errorf("template fail")
}

func TestRunTemplateError(t *testing.T) {

	file := "test_template.txt"

	if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	defer cleanupFile(t, file)

	_, err := run(file, "t", false, &testHTTP{}, &badTemplateGcloud{})

	if err == nil {
		t.Fatal("expected template error")
	}
}

//
// ---------------- WORKER ERROR ----------------
//

type errorHTTP struct{}

func (e *errorHTTP) Do(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("http fail")
}

func TestWorkerHTTPError(t *testing.T) {
	jobs := make(chan string, 1)
	results := make(chan int64, 1)

	jobs <- "test"
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(1)

	go worker(&wg, jobs, results, &errorHTTP{}, "url", "token", time.Tick(time.Millisecond))

	wg.Wait()
	close(results)

	if len(results) != 1 {
		t.Fatal("expected result even on error")
	}
}

func TestWorkerInvalidURL(t *testing.T) {

	jobs := make(chan string, 1)
	results := make(chan int64, 1)

	jobs <- "test"
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(1)

	// invalid URL triggers validation branch
	go worker(&wg, jobs, results, &testHTTP{}, "http://bad-url", "token", time.Tick(time.Millisecond))

	wg.Wait()
	close(results)

	if len(results) != 1 {
		t.Fatal("expected result even with invalid URL")
	}
}

//
// ---------------- EMPTY FILE ----------------
//

func TestRunEmptyFile(t *testing.T) {

	file := "empty.txt"

	if err := os.WriteFile(file, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	defer cleanupFile(t, file)

	client := &testHTTP{}
	gcloud := &mockGcloud{}

	latencies, err := run(file, "t", true, client, gcloud)
	if err != nil {
		t.Fatal(err)
	}

	if len(latencies) != 0 {
		t.Fatal("expected no results")
	}
}

//
// ---------------- URL Validation test ----------------
//

func TestValidateURL(t *testing.T) {

	if err := validateURL("https://modelarmor.test"); err != nil {
		t.Fatal("valid URL rejected")
	}

	if err := validateURL("http://evil.com"); err == nil {
		t.Fatal("invalid URL accepted")
	}
}

type badGcloud struct{}

func (b *badGcloud) GetProject() (string, error)     { return "p", nil }
func (b *badGcloud) GetAccessToken() (string, error) { return "t", nil }
func (b *badGcloud) ValidateTemplate(string, string) error {
	return nil
}

func TestRunInvalidURL(t *testing.T) {

	file := "test_bad_url.txt"
	if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	defer cleanupFile(t, file)

	// force bad URL via template
	_, err := run(file, "://bad", true, &testHTTP{}, &badGcloud{})

	if err == nil {
		t.Fatal("expected URL validation failure")
	}
}

func TestValidateURLEdge(t *testing.T) {
	if err := validateURL(""); err == nil {
		t.Fatal("empty URL should fail")
	}
}

//
// ---------------- File path validation test ----------------
//

func TestValidateFilePath(t *testing.T) {

	if err := validateFilePath("valid.txt"); err != nil {
		t.Fatal("valid path rejected")
	}

	if err := validateFilePath("../secret"); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestReadParagraphsInvalidPath(t *testing.T) {
	_, err := readParagraphs("../badfile.txt")
	if err == nil {
		t.Fatal("expected path validation error")
	}
}

func TestValidateFilePathEdge(t *testing.T) {
	if err := validateFilePath("a/../../b"); err == nil {
		t.Fatal("expected traversal rejection")
	}
}
