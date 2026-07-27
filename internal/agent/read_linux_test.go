//go:build linux

package agent

import "testing"

// TestReadOnRealHost is a smoke test against the actual machine's /proc --
// there is no fixture to assert exact values against, so it checks the
// shape (no error, sane bounds) rather than specific numbers.
func TestReadOnRealHost(t *testing.T) {
	s, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.NumCPU < 1 {
		t.Errorf("NumCPU = %d, want >= 1", s.NumCPU)
	}
	if s.CPUSeconds < 0 {
		t.Errorf("CPUSeconds = %v, want >= 0", s.CPUSeconds)
	}
	if s.MemTotalBytes == 0 {
		t.Error("MemTotalBytes = 0, want a real total")
	}
	if s.MemUsedBytes > s.MemTotalBytes {
		t.Errorf("MemUsedBytes (%d) > MemTotalBytes (%d)", s.MemUsedBytes, s.MemTotalBytes)
	}
	if s.OpenFDs <= 0 {
		t.Errorf("OpenFDs = %d, want > 0 (this test process alone holds some)", s.OpenFDs)
	}
	if s.LoadAvg1 < 0 {
		t.Errorf("LoadAvg1 = %v, want >= 0", s.LoadAvg1)
	}
}

// TestReadCumulativeIncreases confirms CPUSeconds and NetRxBytes/NetTxBytes
// behave as cumulative counters: a later read is never smaller than an
// earlier one (mirrors the "consumer diffs adjacent samples" contract).
func TestReadCumulativeIncreases(t *testing.T) {
	first, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	second, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if second.CPUSeconds < first.CPUSeconds {
		t.Errorf("CPUSeconds went backwards: %v -> %v", first.CPUSeconds, second.CPUSeconds)
	}
	if second.NetRxBytes < first.NetRxBytes {
		t.Errorf("NetRxBytes went backwards: %v -> %v", first.NetRxBytes, second.NetRxBytes)
	}
	if second.NetTxBytes < first.NetTxBytes {
		t.Errorf("NetTxBytes went backwards: %v -> %v", first.NetTxBytes, second.NetTxBytes)
	}
}
