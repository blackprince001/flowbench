package executor

import (
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
)

func TestIsThrottled(t *testing.T) {
	mapped := &ir.ThrottleSpec{Statuses: []int{503}}
	tests := []struct {
		status int
		spec   *ir.ThrottleSpec
		want   bool
	}{
		{429, nil, true},     // 429 always
		{200, nil, false},    //
		{503, nil, false},    // not mapped
		{503, mapped, true},  // author-mapped
		{429, mapped, true},  // 429 still counts alongside a mapping
		{500, mapped, false}, // a different status
	}
	for _, tt := range tests {
		if got := isThrottled(tt.status, tt.spec); got != tt.want {
			t.Errorf("isThrottled(%d, %v) = %v, want %v", tt.status, tt.spec, got, tt.want)
		}
	}
}

func TestThrottleIsError(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		spec *ir.ThrottleSpec
		mode ir.Mode
		want bool
	}{
		{nil, ir.ModeIntegration, true},
		{nil, ir.ModeSystem, true},
		{nil, ir.ModeLoad, false},
		{nil, ir.ModeStress, false},
		{nil, ir.ModeSoak, false},
		{&ir.ThrottleSpec{AsError: &yes}, ir.ModeStress, true},      // override on
		{&ir.ThrottleSpec{AsError: &no}, ir.ModeIntegration, false}, // override off
	}
	for _, tt := range tests {
		if got := throttleIsError(tt.spec, tt.mode); got != tt.want {
			t.Errorf("throttleIsError(%v, %q) = %v, want %v", tt.spec, tt.mode, got, tt.want)
		}
	}
}
