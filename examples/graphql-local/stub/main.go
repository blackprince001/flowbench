// A small GraphQL service — deliberately hand-rolled rather than schema-driven,
// since the point is the wire contract, not a real graph.
//
// The behaviour that matters: every response below is `200 OK`. GraphQL puts
// the transport's verdict in the status and the operation's verdict in the
// body, so a flow that classified on status alone would score all four of
// these as passes.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
)

type operation struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
	Operation string         `json:"operationName"`
}

var orders atomic.Int64

func main() {
	http.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var op operation
		if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
			write(w, `{"errors":[{"message":"request body is not a GraphQL operation"}]}`)
			return
		}

		switch {
		// A restricted field: the operation resolved nothing at all.
		case strings.Contains(op.Query, "costPriceCents"):
			write(w, `{"data":null,"errors":[
				{"message":"field 'costPriceCents' is restricted to internal clients",
				 "path":["product","costPriceCents"]}]}`)

		// A federated partial: the product subgraph answered, reviews did not.
		case strings.Contains(op.Query, "reviews"):
			write(w, `{"data":{"product":{"id":"prod_42","name":"Flowbench Tee"},"reviews":null},
				"errors":[{"message":"reviews subgraph timed out","path":["reviews"]}]}`)

		case strings.Contains(op.Query, "placeOrder"):
			// The variables arrive typed, so an id is a string and a quantity is
			// a number — which is the whole reason values travel as variables
			// rather than spliced into the document.
			id, _ := op.Variables["productId"].(string)
			quantity, _ := op.Variables["quantity"].(float64)
			if id == "" || quantity <= 0 {
				write(w, `{"data":null,"errors":[{"message":"placeOrder needs a productId and a positive quantity"}]}`)
				return
			}
			n := orders.Add(1)
			writef(w, `{"data":{"placeOrder":{"id":"ord_%04d","status":"PENDING","quantity":%d}}}`, n, int(quantity))

		case strings.Contains(op.Query, "product"):
			sku, _ := op.Variables["sku"].(string)
			if sku != "FB-001" {
				writef(w, `{"data":null,"errors":[{"message":"no product with sku %q","path":["product"]}]}`, sku)
				return
			}
			write(w, `{"data":{"product":{"id":"prod_42","name":"Flowbench Tee","priceCents":2500}}}`)

		default:
			write(w, `{"errors":[{"message":"unknown operation"}]}`)
		}
	})

	log.Println("graphql stub listening on :8091 — POST /graphql")
	log.Println("  FindProduct / PlaceOrder resolve; Restricted errors; Reviews returns a partial")
	log.Fatal(http.ListenAndServe(":8091", nil))
}

// write answers 200 always: in GraphQL the status is the transport's verdict,
// not the operation's.
func write(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(body))
}

func writef(w http.ResponseWriter, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, format, args...)
}
