package executor

import (
	"fmt"
	"strings"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/ir"
)

// graphqlErrorSpan is the structural name a GraphQL failure folds under. It is
// a sibling of the assert_* children, so a run where one query kept erroring
// shows that in the flame graph rather than only in a failure list.
const graphqlErrorSpan = "graphql_errors"

// maxReportedErrors bounds a recorded failure detail. A single malformed query
// can return an error per field, and every one of these is stored.
const maxReportedErrors = 3

// graphQLCheck turns the spec's error policy into the reading the executor
// applies to a response body.
//
// This is the one place GraphQL genuinely departs from HTTP. Everywhere else
// the transport status classifies the step; here a failed operation answers
// `200 OK` and reports the failure in `errors`. Classifying off the status
// alone would score every broken query as a pass, so by default a non-empty
// `errors` array fails the step — the flow author has to opt out, rather than
// having to remember to opt in with an assertion.
func graphQLCheck(spec *ir.GraphQLSpec) bodyCheck {
	return func(body []byte) (string, string) {
		// ignore hands GraphQL semantics back to the flow's own assertions,
		// including the judgement of whether the body is a GraphQL document.
		if spec.OnErrors == ir.GraphQLErrorsIgnore {
			return "", ""
		}

		res := adapters.ReadGraphQLResult(body)
		if res.Malformed != nil {
			return graphqlErrorSpan, fmt.Sprintf("graphql: %v", res.Malformed)
		}
		if len(res.Errors) == 0 {
			return "", ""
		}
		// A partial success still resolved something; the federated case where
		// one subgraph fails and the rest answer is a normal response, not a
		// broken run.
		if spec.OnErrors == ir.GraphQLErrorsAllowPartial && res.HasData {
			return "", ""
		}
		return graphqlErrorSpan, "graphql: " + summarize(res.Errors)
	}
}

func summarize(errs []string) string {
	if len(errs) <= maxReportedErrors {
		return strings.Join(errs, "; ")
	}
	return fmt.Sprintf("%s (+%d more)",
		strings.Join(errs[:maxReportedErrors], "; "), len(errs)-maxReportedErrors)
}
