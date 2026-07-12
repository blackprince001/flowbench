// Package parser turns authoring inputs (YAML files; IR emitted by the Python
// SDK) into the validated canonical flow IR. It owns schema validation,
// variable-graph resolution (every template reference must have an upstream
// extraction or data pool), endpoint reference checks, profile sanity, and
// retry-policy sanity — reported as pre-run errors with file/line context.
// It also warns when a step rename would silently break cross-run folding
// (PRD 10.7).
package parser
