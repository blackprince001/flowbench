package eval

import (
	"fmt"

	"github.com/blackprince001/flowbench/internal/ir"
)

// Extract pulls the value an extraction names from the target. The bool is
// false when the source is absent (a missing header, an unmatched body path).
func Extract(ex ir.Extraction, t Target) (any, bool, error) {
	switch ex.From {
	case "", ir.ExtractBody:
		return queryJSON(t.Body(), ex.Path)
	case ir.ExtractHeader:
		v, ok := t.Header(ex.Path)
		return v, ok, nil
	case ir.ExtractStatus:
		return t.Status(), true, nil
	default:
		return nil, false, fmt.Errorf("unknown extraction source %q", ex.From)
	}
}
