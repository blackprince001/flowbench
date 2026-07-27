// A billing service over gRPC, small enough to read in one sitting.
//
// Two behaviours matter. It is *stateful* — a charge lives on the server under
// an id, and a refund of an id it never issued is NOT_FOUND — so the chaining
// flow really does depend on the value it extracted rather than on a fixed
// answer. And when too many charges are in flight it sheds them with
// RESOURCE_EXHAUSTED, gRPC's own "you are going too fast": a status in a
// numbering that shares nothing with HTTP's, which the engine still has to
// classify as `throttled` rather than as an error.
//
// Nothing here is generated. The stub compiles ../proto/billing.proto at
// startup and serves the descriptors directly (internal/grpcstub), which is
// the same trick the engine uses to make the calls — so there is no protoc in
// anyone's path and no checked-in build step.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/blackprince001/flowbench/internal/grpcstub"
)

// capacity is how many charges the service processes at once. Past it, a
// charge is shed rather than queued — the interesting case, because the
// connection is long since established and the call reached the service.
const capacity = 30

// work is how long a charge takes. Without it nothing is ever concurrent and
// the capacity limit would never engage.
const work = 20 * time.Millisecond

const addr = "127.0.0.1:50051"

var (
	inFlight atomic.Int64
	issued   atomic.Int64
	shed     atomic.Int64

	mu      sync.Mutex
	charges = map[string]charge{}
)

type charge struct {
	Account     string
	AmountCents string
	Currency    string
	Status      string
}

func main() {
	srv, err := grpcstub.New("examples/grpc-local/proto/billing.proto")
	if err != nil {
		log.Fatalf("billing stub: %v", err)
	}
	srv.Handle("billing.v1.Billing/Charge", handleCharge)
	srv.Handle("billing.v1.Billing/Refund", handleRefund)
	srv.Handle("billing.v1.Billing/GetCharge", handleGetCharge)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("billing stub: %v", err)
	}
	go report()
	log.Printf("billing stub on %s (capacity %d concurrent charges)", addr, capacity)
	if err := srv.Serve(ln); err != nil {
		log.Fatalf("billing stub: %v", err)
	}
}

// handleCharge is the load-shedding one. The limit is on calls *in flight*, so
// a flow that stays under it never sees a throttle and one that goes over sees
// them steadily — which is what makes throttle_rate a number worth reading.
func handleCharge(ctx context.Context, req []byte) ([]byte, error) {
	n := inFlight.Add(1)
	defer inFlight.Add(-1)
	if n > capacity {
		shed.Add(1)
		// retry-after is the server saying how long it wanted to be left
		// alone. gRPC has no standard header for it; this is the spelling
		// services borrow from HTTP, and the engine reads it.
		grpc.SetTrailer(ctx, metadata.Pairs("retry-after", "1"))
		return nil, status.Errorf(codes.ResourceExhausted,
			"%d charges in flight, capacity is %d", n, capacity)
	}

	var in struct {
		Account     string `json:"account"`
		AmountCents string `json:"amountCents"`
		Currency    string `json:"currency"`
	}
	if err := json.Unmarshal(req, &in); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if in.Account == "" {
		return nil, status.Error(codes.InvalidArgument, "account is required")
	}

	time.Sleep(work)

	id := fmt.Sprintf("ch_%06d", issued.Add(1))
	mu.Lock()
	charges[id] = charge{
		Account: in.Account, AmountCents: in.AmountCents,
		Currency: in.Currency, Status: "captured",
	}
	mu.Unlock()

	return []byte(fmt.Sprintf(
		`{"chargeId":%q,"status":"captured","amountCents":%q,"currency":%q}`,
		id, in.AmountCents, in.Currency)), nil
}

// handleRefund is the stateful one: it only knows about ids it issued, so a
// flow that refunds a value it did not extract gets NOT_FOUND.
func handleRefund(_ context.Context, req []byte) ([]byte, error) {
	var in struct {
		ChargeID string `json:"chargeId"`
	}
	if err := json.Unmarshal(req, &in); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	c, ok := charges[in.ChargeID]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no charge %s", in.ChargeID)
	}
	if c.Status == "refunded" {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is already refunded", in.ChargeID)
	}
	c.Status = "refunded"
	charges[in.ChargeID] = c

	return []byte(fmt.Sprintf(`{"chargeId":%q,"status":"refunded"}`, in.ChargeID)), nil
}

func handleGetCharge(_ context.Context, req []byte) ([]byte, error) {
	var in struct {
		ChargeID string `json:"chargeId"`
	}
	if err := json.Unmarshal(req, &in); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	c, ok := charges[in.ChargeID]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no charge %s", in.ChargeID)
	}
	return []byte(fmt.Sprintf(
		`{"chargeId":%q,"status":%q,"amountCents":%q,"currency":%q}`,
		in.ChargeID, c.Status, c.AmountCents, c.Currency)), nil
}

func report() {
	for range time.Tick(time.Second) {
		log.Printf("in flight %d · issued %d · shed %d", inFlight.Load(), issued.Load(), shed.Load())
	}
}
