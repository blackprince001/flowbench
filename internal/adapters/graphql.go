package adapters

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/blackprince001/flowbench/internal/ir"
)

// GraphQL rides on the HTTP adapter rather than beside it: an operation is a
// POST of {query, variables, operationName}, so the session, cookie jar,
// per-phase spans, retry policy, throttle classification, and auth are the
// same code path. What this file adds is the request body shape and the one
// place GraphQL genuinely differs — a failed operation answers `200 OK` and
// says so in the body, so the outcome has to be read from `errors`.

// BuildGraphQLRequest turns an operation into the HTTP request that carries
// it. Variables are templated with the JSON-escaping resolver, the same one
// call bodies use, so a quote or newline in an extracted value cannot break
// out of the document.
func BuildGraphQLRequest(spec *ir.GraphQLSpec, resolve Resolver) (*Request, error) {
	url, err := ir.ExpandTemplates(spec.URL, resolve)
	if err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}

	// The query is not templated: values reach an operation as variables, so
	// splicing them into the document would sidestep the server's own typing
	// and escaping.
	payload := map[string]any{"query": spec.Query}
	if spec.Operation != "" {
		payload["operationName"] = spec.Operation
	}
	if len(spec.Variables) > 0 {
		expanded, err := ir.ExpandTemplates(string(spec.Variables), jsonEscaped(resolve))
		if err != nil {
			return nil, fmt.Errorf("variables: %w", err)
		}
		var vars map[string]any
		if err := json.Unmarshal([]byte(expanded), &vars); err != nil {
			return nil, fmt.Errorf("variables are not a JSON object after templating: %w", err)
		}
		payload["variables"] = vars
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encoding operation: %w", err)
	}

	req := &Request{Method: "POST", URL: url, Body: body}
	req.SetHeader("Content-Type", "application/json")
	req.SetHeader("Accept", "application/json")
	for k, v := range spec.Headers {
		value, err := ir.ExpandTemplates(v, resolve)
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", k, err)
		}
		req.SetHeader(k, value)
	}
	return req, nil
}

// GraphQLResult is what a response says about the operation, as opposed to
// what the transport says about the request.
type GraphQLResult struct {
	// Errors are the operation's own errors, already flattened to their
	// messages. Empty means the operation reported none.
	Errors []string
	// HasData reports whether `data` came back non-null. A response carrying
	// both data and errors is a partial success, which federated graphs return
	// routinely when one subgraph fails and the rest resolve.
	HasData bool
	// Malformed is set when the body is not a GraphQL response at all — an
	// HTML error page from a proxy, say. Callers treat it as a failure whatever
	// the error policy says, since nothing can be extracted from it.
	Malformed error
}

// ReadGraphQLResult inspects a response body for the data/errors shape.
func ReadGraphQLResult(body []byte) GraphQLResult {
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Path    []any  `json:"path"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return GraphQLResult{Malformed: fmt.Errorf("response is not a GraphQL document: %w", err)}
	}

	res := GraphQLResult{
		HasData: len(envelope.Data) > 0 && string(envelope.Data) != "null",
	}
	for _, e := range envelope.Errors {
		message := e.Message
		if message == "" {
			message = "(no message)"
		}
		if len(e.Path) > 0 {
			message = fmt.Sprintf("%s (at %s)", message, joinPath(e.Path))
		}
		res.Errors = append(res.Errors, message)
	}
	return res
}

// joinPath renders an error's response path — the field that failed — as the
// dotted form a reader would use to find it in the document.
func joinPath(path []any) string {
	var out strings.Builder
	for i, segment := range path {
		switch v := segment.(type) {
		case string:
			if i > 0 {
				out.WriteString(".")
			}
			out.WriteString(v)
		case float64:
			fmt.Fprintf(&out, "[%d]", int(v))
		default:
			fmt.Fprintf(&out, "%v", v)
		}
	}
	return out.String()
}
