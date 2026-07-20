package parser_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/parser"
)

func TestParseTargetFile(t *testing.T) {
	tc, err := parser.ParseTargetFile(filepath.Join("..", "..", "tests", "targets", "local.yaml"))
	if err != nil {
		t.Fatalf("ParseTargetFile: %v", err)
	}
	if tc.Name != "local" {
		t.Errorf("name = %q", tc.Name)
	}
	if len(tc.BaseURLs) != 1 || tc.BaseURLs[0] != "http://localhost:8080" {
		t.Errorf("base_urls = %v", tc.BaseURLs)
	}
	if tc.MaxVUs != 200 || tc.MaxRPS != 500 {
		t.Errorf("ceilings = %d vus / %d rps", tc.MaxVUs, tc.MaxRPS)
	}
}

func TestParseTargetRejectsUnknownKey(t *testing.T) {
	_, err := parser.ParseTarget([]byte("name: x\nbase_urls: [http://h]\nbogus: 1\n"), "t.yaml")
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("unknown key should fail loudly, got %v", err)
	}
}

func TestParseTargetRejectsRelativeBaseURL(t *testing.T) {
	_, err := parser.ParseTarget([]byte("name: x\nbase_urls: [notaurl]\n"), "t.yaml")
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("relative base URL should be rejected, got %v", err)
	}
}
