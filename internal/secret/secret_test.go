package secret_test

import (
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/secret"
)

func TestRedactReplacesEverywhere(t *testing.T) {
	s := secret.NewSet()
	s.Add("s3cr3t-token")

	in := `Authorization: Bearer s3cr3t-token, retry with s3cr3t-token`
	out := s.Redact(in)
	if strings.Contains(out, "s3cr3t-token") {
		t.Fatalf("secret survived redaction: %q", out)
	}
	if strings.Count(out, secret.Placeholder) != 2 {
		t.Errorf("want both occurrences redacted, got %q", out)
	}
}

func TestEmptyValueIsIgnored(t *testing.T) {
	s := secret.NewSet()
	s.Add("")
	if got := s.Redact("nothing to hide"); got != "nothing to hide" {
		t.Errorf("empty secret must not redact anything, got %q", got)
	}
}

func TestOverlappingSecretsRedactLongestFirst(t *testing.T) {
	s := secret.NewSet()
	s.Add("abc")
	s.Add("abcdef")
	out := s.Redact("value=abcdef")
	if strings.Contains(out, "abc") || strings.Contains(out, "def") {
		t.Fatalf("longer secret should be fully redacted, got %q", out)
	}
	if out != "value="+secret.Placeholder {
		t.Errorf("got %q", out)
	}
}

func TestContainsAndValues(t *testing.T) {
	s := secret.NewSet()
	s.Add("one")
	s.Add("two")
	if !s.Contains("one") || s.Contains("three") {
		t.Errorf("Contains wrong: %v %v", s.Contains("one"), s.Contains("three"))
	}
	if len(s.Values()) != 2 {
		t.Errorf("want 2 values, got %v", s.Values())
	}
}
