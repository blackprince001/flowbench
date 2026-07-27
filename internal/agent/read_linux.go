//go:build linux

package agent

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// clkTck is sysconf(_SC_CLK_TCK): the units /proc/stat's jiffie counters are
// in. Reading it properly needs a syscall (or cgo); 100 is the value on
// every mainstream Linux platform this toolkit targets (x86, x86_64, arm64),
// so it is hardcoded rather than pulling in a dependency for it — a
// documented simplification, not an oversight.
const clkTck = 100.0

// Read samples the host's current resource use by parsing /proc — no
// third-party dependency, matching the project's existing stdlib/syscall-only
// convention for OS-facing code (internal/executor/cpu_unix.go).
func Read() (Sample, error) {
	cpu, err := readCPU()
	if err != nil {
		return Sample{}, fmt.Errorf("read cpu: %w", err)
	}
	memUsed, memTotal, err := readMem()
	if err != nil {
		return Sample{}, fmt.Errorf("read mem: %w", err)
	}
	rx, tx, err := readNet()
	if err != nil {
		return Sample{}, fmt.Errorf("read net: %w", err)
	}
	fds, err := readOpenFDs()
	if err != nil {
		return Sample{}, fmt.Errorf("read open fds: %w", err)
	}
	load1, err := readLoadAvg1()
	if err != nil {
		return Sample{}, fmt.Errorf("read load average: %w", err)
	}

	return Sample{
		CPUSeconds:    cpu,
		NumCPU:        runtime.NumCPU(),
		MemUsedBytes:  memUsed,
		MemTotalBytes: memTotal,
		NetRxBytes:    rx,
		NetTxBytes:    tx,
		OpenFDs:       fds,
		LoadAvg1:      load1,
	}, nil
}

// readCPU sums /proc/stat's aggregate "cpu " line, excluding idle and
// iowait, into cumulative busy CPU-seconds across every core since boot.
func readCPU() (float64, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, fmt.Errorf("empty /proc/stat")
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, fmt.Errorf("unexpected /proc/stat first line %q", sc.Text())
	}

	var jiffies []float64
	for _, f := range fields[1:] {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return 0, fmt.Errorf("parse /proc/stat field %q: %w", f, err)
		}
		jiffies = append(jiffies, v)
	}
	// Order: user, nice, system, idle, iowait, irq, softirq, steal, guest,
	// guest_nice. idle (index 3) and iowait (index 4) are not busy time.
	var busy float64
	for i, v := range jiffies {
		if i == 3 || i == 4 {
			continue
		}
		busy += v
	}
	return busy / clkTck, nil
}

// readMem parses MemTotal/MemAvailable out of /proc/meminfo (kB) into bytes.
// MemAvailable (not MemFree) is used for "used", matching what tools like
// `free -h` report as actually available to new allocations.
func readMem() (used, total uint64, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	var memTotal, memAvailable uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			memTotal, err = parseMeminfoKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			memAvailable, err = parseMeminfoKB(line)
		}
		if err != nil {
			return 0, 0, err
		}
	}
	if memTotal == 0 {
		return 0, 0, fmt.Errorf("MemTotal not found in /proc/meminfo")
	}
	if memAvailable > memTotal {
		memAvailable = memTotal
	}
	return (memTotal - memAvailable) * 1024, memTotal * 1024, nil
}

func parseMeminfoKB(line string) (uint64, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, fmt.Errorf("malformed /proc/meminfo line %q", line)
	}
	return strconv.ParseUint(fields[1], 10, 64)
}

// readNet sums received/transmitted bytes across every interface except
// loopback, from /proc/net/dev's fixed column layout.
func readNet() (rx, tx uint64, err error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Scan() // header line 1
	sc.Scan() // header line 2
	for sc.Scan() {
		line := sc.Text()
		iface, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		iface = strings.TrimSpace(iface)
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		rxB, err1 := strconv.ParseUint(fields[0], 10, 64)
		txB, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue // a malformed interface line shouldn't fail the whole sample
		}
		rx += rxB
		tx += txB
	}
	return rx, tx, nil
}

// readOpenFDs reads the system-wide allocated file-handle count, the first
// field of /proc/sys/fs/file-nr.
func readOpenFDs() (int, error) {
	b, err := os.ReadFile("/proc/sys/fs/file-nr")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 1 {
		return 0, fmt.Errorf("malformed /proc/sys/fs/file-nr")
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, err
	}
	return n, nil
}

// readLoadAvg1 reads the 1-minute load average, the first field of
// /proc/loadavg.
func readLoadAvg1() (float64, error) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 1 {
		return 0, fmt.Errorf("malformed /proc/loadavg")
	}
	return strconv.ParseFloat(fields[0], 64)
}
