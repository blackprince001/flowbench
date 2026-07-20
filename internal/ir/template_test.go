package ir_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
)

func TestExpandTemplates(t *testing.T) {
	resolve := func(ref string) (string, error) {
		vals := map[string]string{
			"token":      "tok-123",
			"user.email": "ada@example.test",
		}
		v, ok := vals[ref]
		if !ok {
			return "", errors.New("unknown ref")
		}
		return v, nil
	}

	got, err := ir.ExpandTemplates("Bearer {{ token }} for {{ user.email }}", resolve)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if got != "Bearer tok-123 for ada@example.test" {
		t.Errorf("expanded = %q", got)
	}

	if _, err := ir.ExpandTemplates("x {{ missing }}", resolve); err == nil ||
		!strings.Contains(err.Error(), "missing") {
		t.Errorf("unresolved ref should error naming the ref, got: %v", err)
	}

	// malformed placeholders pass through untouched; validation catches them pre-run
	got, err = ir.ExpandTemplates("keep {{ user..email }} verbatim", resolve)
	if err != nil || got != "keep {{ user..email }} verbatim" {
		t.Errorf("malformed placeholder should pass through, got %q, %v", got, err)
	}
}
