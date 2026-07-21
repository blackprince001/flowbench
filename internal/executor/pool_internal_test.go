package executor

import (
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/planner"
)

func TestCurveAt(t *testing.T) {
	segs := []planner.Segment{
		{Kind: planner.Ramp, StartVUs: 0, EndVUs: 100, Duration: ir.Duration(10 * time.Second)},
		{Kind: planner.Hold, StartVUs: 100, EndVUs: 100, Duration: ir.Duration(10 * time.Second)},
	}
	tests := []struct {
		at   time.Duration
		want int
	}{
		{0, 0},
		{5 * time.Second, 50},
		{10 * time.Second, 100},
		{15 * time.Second, 100},
		{25 * time.Second, 100}, // past the end holds at the last level
	}
	for _, tt := range tests {
		if got := curveAt(segs, tt.at); got != tt.want {
			t.Errorf("curveAt(%s) = %d, want %d", tt.at, got, tt.want)
		}
	}

	if got := curveAt(nil, time.Second); got != 0 {
		t.Errorf("curveAt(nil) = %d, want 0", got)
	}
}

func TestSpawnTick(t *testing.T) {
	if got := spawnTick(5*time.Minute, 500); got > 200*time.Millisecond {
		t.Errorf("spawnTick capped high: got %s", got)
	}
	if got := spawnTick(10*time.Millisecond, 100); got < 5*time.Millisecond {
		t.Errorf("spawnTick floored low: got %s", got)
	}
	if got := spawnTick(0, 0); got != 50*time.Millisecond {
		t.Errorf("spawnTick(0,0) = %s, want 50ms", got)
	}
}
