package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func server(t *testing.T, code int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestAllUpExitsZero(t *testing.T) {
	srv := server(t, http.StatusOK)
	code, out, _ := runCLI(srv.URL, srv.URL)
	if code != exitOK {
		t.Fatalf("exit %d, want %d; output:\n%s", code, exitOK, out)
	}
	if !strings.Contains(out, "2/2 up") {
		t.Fatalf("summary missing from output:\n%s", out)
	}
}

func TestAnyDownExitsOne(t *testing.T) {
	ok := server(t, http.StatusOK)
	bad := server(t, http.StatusInternalServerError)
	code, out, _ := runCLI("-retries", "0", ok.URL, bad.URL)
	if code != exitDown {
		t.Fatalf("exit %d, want %d; output:\n%s", code, exitDown, out)
	}
	if !strings.Contains(out, "DOWN") || !strings.Contains(out, "1/2 up") {
		t.Fatalf("expected one DOWN row and 1/2 up:\n%s", out)
	}
}

func TestAllDownOmitsLatency(t *testing.T) {
	bad := server(t, http.StatusServiceUnavailable)
	code, out, _ := runCLI("-retries", "0", bad.URL)
	if code != exitDown || !strings.Contains(out, "0/1 up") || strings.Contains(out, "p50") {
		t.Fatalf("exit %d; want 0/1 up with no fake latency line:\n%s", code, out)
	}
}

func TestJSONOutput(t *testing.T) {
	srv := server(t, http.StatusOK)
	code, out, _ := runCLI("-o", "json", srv.URL)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	var rep jsonReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if rep.Summary.Total != 1 || rep.Summary.Up != 1 || len(rep.Results) != 1 || rep.Results[0].StatusCode != 200 {
		t.Fatalf("unexpected report: %+v", rep)
	}
}

func TestTargetsFile(t *testing.T) {
	ok := server(t, http.StatusNoContent)
	path := filepath.Join(t.TempDir(), "targets.json")
	cfg := `[{"name": "api", "url": "` + ok.URL + `", "expect_status": 204}]`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLI("-f", path)
	if code != exitOK || !strings.Contains(out, "api") {
		t.Fatalf("exit %d; output:\n%s", code, out)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	cases := map[string][]string{
		"no targets":     {},
		"bad format":     {"-o", "yaml", "https://example.com"},
		"bad url":        {"ftp://example.com"},
		"bad concurrent": {"-c", "0", "https://example.com"},
		"missing file":   {"-f", "/does/not/exist.json"},
		"unknown flag":   {"-nope"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if code, _, _ := runCLI(args...); code != exitUsage {
				t.Fatalf("exit %d, want %d", code, exitUsage)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := runCLI("-version")
	if code != exitOK || !strings.HasPrefix(out, "pulsecheck ") {
		t.Fatalf("exit %d, output %q", code, out)
	}
}
