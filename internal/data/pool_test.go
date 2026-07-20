package data_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/blackprince001/flowbench/internal/data"
	"github.com/blackprince001/flowbench/internal/ir"
)

func idRows(n int) []data.Row {
	rows := make([]data.Row, n)
	for i := range rows {
		rows[i] = data.Row{"id": strconv.Itoa(i)}
	}
	return rows
}

// TestUniquePerVUNeverRepeatsUnderConcurrency is the issue #7 acceptance: N
// concurrent consumers of a unique-per-vu pool never receive the same row.
func TestUniquePerVUNeverRepeatsUnderConcurrency(t *testing.T) {
	const n = 300
	p, err := data.New(ir.DataPool{Name: "u", Distribution: ir.DistributeUniquePerVU}, idRows(n), 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			row, ok, err := p.Next()
			if err != nil || !ok {
				t.Errorf("draw %d: ok=%v err=%v", i, ok, err)
				return
			}
			got[i] = row["id"]
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("row %q was handed to two consumers", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Errorf("want %d distinct rows, got %d", n, len(seen))
	}

	// The pool is now exhausted; the default policy fails.
	if _, _, err := p.Next(); !errors.Is(err, data.ErrExhausted) {
		t.Errorf("want ErrExhausted after every row is drawn, got %v", err)
	}
}

func TestExhaustionPolicies(t *testing.T) {
	drawTwice := func(p *data.Pool) {
		for i := 0; i < 2; i++ {
			if _, _, err := p.Next(); err != nil {
				t.Fatalf("draw %d: %v", i, err)
			}
		}
	}

	t.Run("fail", func(t *testing.T) {
		p, _ := data.New(ir.DataPool{Name: "p", OnExhausted: ir.ExhaustFail}, idRows(2), 1)
		drawTwice(p)
		if _, _, err := p.Next(); !errors.Is(err, data.ErrExhausted) {
			t.Errorf("want ErrExhausted, got %v", err)
		}
	})

	t.Run("cycle", func(t *testing.T) {
		p, _ := data.New(ir.DataPool{Name: "p", OnExhausted: ir.ExhaustCycle}, idRows(2), 1)
		drawTwice(p)
		row, ok, err := p.Next()
		if err != nil || !ok || row["id"] != "0" {
			t.Errorf("cycle should wrap to row 0, got %v ok=%v err=%v", row, ok, err)
		}
	})

	t.Run("stop", func(t *testing.T) {
		p, _ := data.New(ir.DataPool{Name: "p", OnExhausted: ir.ExhaustStop}, idRows(2), 1)
		drawTwice(p)
		if row, ok, err := p.Next(); ok || err != nil || row != nil {
			t.Errorf("stop should signal done, got %v ok=%v err=%v", row, ok, err)
		}
	})
}

func TestRoundRobinCyclesForever(t *testing.T) {
	p, _ := data.New(ir.DataPool{Name: "r", Distribution: ir.DistributeRoundRobin}, idRows(3), 1)
	want := []string{"0", "1", "2", "0", "1", "2", "0"}
	for i, w := range want {
		row, ok, err := p.Next()
		if err != nil || !ok || row["id"] != w {
			t.Fatalf("draw %d = %v (ok=%v err=%v), want id %s", i, row, ok, err, w)
		}
	}
}

func TestRandomIsSeedReproducible(t *testing.T) {
	dp := ir.DataPool{Name: "rnd", Distribution: ir.DistributeRandom}
	seq := func(seed int64) []string {
		p, _ := data.New(dp, idRows(10), seed)
		out := make([]string, 20)
		for i := range out {
			row, _, _ := p.Next()
			out[i] = row["id"]
		}
		return out
	}

	a, b := seq(42), seq(42)
	if !equal(a, b) {
		t.Errorf("same seed must reproduce the same draws:\n%v\n%v", a, b)
	}
	if equal(seq(42), seq(7)) {
		t.Errorf("different seeds should not produce identical draws")
	}
}

func TestLoadCSVFromRealFixture(t *testing.T) {
	// The checked-in fixture carries a JSON array in a quoted CSV field.
	p, err := data.Load(ir.DataPool{Name: "user", Source: "users.csv"},
		filepath.Join("..", "..", "tests", "fixtures"), 1)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Len() != 3 {
		t.Fatalf("want 3 rows, got %d", p.Len())
	}
	row, _, _ := p.Next()
	if row["email"] != "ada@example.test" || row["password"] != "correct-horse-1" {
		t.Errorf("first row = %v", row)
	}
	if row["cart"] != `["sku-100","sku-204"]` {
		t.Errorf("quoted JSON array field mis-parsed: %q", row["cart"])
	}
}

func TestLoadJSONArrayField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	os.WriteFile(path, []byte(`[{"email":"a@b.test","cart":["sku-1","sku-2"],"vip":true,"credits":5}]`), 0o600)

	p, err := data.Load(ir.DataPool{Name: "user", Source: "users.json"}, dir, 1)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	row, _, _ := p.Next()
	if row["email"] != "a@b.test" || row["cart"] != `["sku-1","sku-2"]` {
		t.Errorf("row = %v", row)
	}
	if row["vip"] != "true" || row["credits"] != "5" {
		t.Errorf("scalar coercion wrong: vip=%q credits=%q", row["vip"], row["credits"])
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := data.Load(ir.DataPool{Name: "x", Source: "nope.csv"}, t.TempDir(), 1); err == nil {
		t.Error("missing file should error")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "rows.dat")
	os.WriteFile(path, []byte("a,b\n1,2\n"), 0o600)
	if _, err := data.Load(ir.DataPool{Name: "x", Source: "rows.dat"}, dir, 1); err == nil {
		t.Error("unknown extension without an explicit format should error")
	}

	empty := filepath.Join(dir, "empty.json")
	os.WriteFile(empty, []byte(`[]`), 0o600)
	if _, err := data.Load(ir.DataPool{Name: "x", Source: "empty.json"}, dir, 1); err == nil {
		t.Error("a pool with no rows should error")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
