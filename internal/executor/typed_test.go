package executor_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
)

// echoFlow extracts from /source, then posts body to /echo, and returns what
// /echo received, decoded.
func echoFlow(t *testing.T, scope *executor.Scope, body string) (map[string]any, *executor.Iteration, error) {
	t.Helper()
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/source" {
			w.Write([]byte(`{"n": 42, "ok": true, "items": ["a", "b"], "obj": {"k": 1}, "none": null, "s": "7"}`))
			return
		}
		got, _ = io.ReadAll(r.Body)
		w.Write(got)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "f", Steps: []ir.Step{
		{ID: "source", Type: ir.StepCall, Call: &ir.CallSpec{Method: "GET", URL: "/source"},
			Extract: []ir.Extraction{
				{Var: "n", Path: "$.n"}, {Var: "ok", Path: "$.ok"}, {Var: "items", Path: "$.items"},
				{Var: "obj", Path: "$.obj"}, {Var: "none", Path: "$.none"}, {Var: "s", Path: "$.s"},
			}},
		{ID: "echo", Type: ir.StepCall, Call: &ir.CallSpec{Method: "POST", URL: "/echo", Body: json.RawMessage(body)}},
	}}
	r := &executor.Runner{Session: adapters.NewSession(adapters.SessionOptions{}), BaseURL: srv.URL, Mode: ir.ModeIntegration}
	it, err := r.RunFlow(context.Background(), flow, scope)
	if err != nil || got == nil {
		return nil, it, err
	}
	var out map[string]any
	if jerr := json.Unmarshal(got, &out); jerr != nil {
		t.Fatalf("the engine sent invalid JSON %s: %v", got, jerr)
	}
	return out, it, nil
}

// TestWholeValueTemplatesKeepTheirType is the #101 acceptance: a value that
// is exactly one template arrives with the type it was extracted with.
func TestWholeValueTemplatesKeepTheirType(t *testing.T) {
	got, _, err := echoFlow(t, executor.NewScope("", nil), `{
		"n": "{{ n }}", "ok": "{{ ok }}", "items": "{{ items }}", "obj": "{{ obj }}",
		"none": "{{ none }}", "s": "{{ s }}",
		"mixed": "id-{{ n }}", "nested": {"deep": ["{{ n }}"]}, "{{ s }}": "key stays text"}`)
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	want := map[string]any{
		"n": 42.0, "ok": true, "items": []any{"a", "b"}, "obj": map[string]any{"k": 1.0},
		"none": nil, "s": "7",
		"mixed": "id-42", "nested": map[string]any{"deep": []any{42.0}}, "7": "key stays text",
	}
	for k, w := range want {
		wb, _ := json.Marshal(w)
		gb, _ := json.Marshal(got[k])
		if string(wb) != string(gb) {
			t.Errorf("%s = %s, want %s", k, gb, wb)
		}
	}
}

// TestPoolAndEnvValuesStayStrings covers F2 and F4: a CSV cell and an env
// value are text, so they arrive as strings, and the env value is still a
// secret.
func TestPoolAndEnvValuesStayStrings(t *testing.T) {
	const secret = "tok-0042-do-not-leak"
	t.Setenv("FLOWBENCH_TEST_SECRET", secret)
	scope := executor.NewScope("user", map[string]string{"zip": "007", "count": "3"})
	got, _, err := echoFlow(t, scope, `{"zip": "{{ user.zip }}", "count": "{{ user.count }}", "key": "{{ env.FLOWBENCH_TEST_SECRET }}"}`)
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if got["zip"] != "007" || got["count"] != "3" || got["key"] != secret {
		t.Errorf("got %v, want zip and count as strings and the env value as text", got)
	}
	if !scope.Secrets().Contains(secret) {
		t.Error("an env value injected as a whole value must still be registered for redaction")
	}
}

// TestMissingWholeValueIsAStepError covers F3: a missing variable stops the
// step with an error, never a malformed body.
func TestMissingWholeValueIsAStepError(t *testing.T) {
	_, _, err := echoFlow(t, executor.NewScope("", nil), `{"x": "{{ nothing }}"}`)
	if err == nil || !strings.Contains(err.Error(), "nothing") {
		t.Fatalf("want an error naming the missing variable, got %v", err)
	}
}
