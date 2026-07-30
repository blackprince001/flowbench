package adapters

import (
	"testing"
	"time"

	"google.golang.org/grpc/stats"

	"github.com/blackprince001/flowbench/internal/span"
)

// The phase events do not arrive in the order they happen. grpc-go writes the
// request to the transport and only then calls the stats handler for
// OutPayload, while InHeader lands on the transport's reader goroutine — so a
// fast target can be observed answering before the send it answers. These
// tests pin both orders, because the racing one is not reproducible on demand:
// it cost a CI failure where the leg carried connect alone.

func phaseNames(leg *span.Span) []string {
	names := make([]string, 0, len(leg.Children))
	for _, c := range leg.Children {
		names = append(names, c.Name)
	}
	return names
}

func has(leg *span.Span, name string) bool {
	for _, c := range leg.Children {
		if c.Name == name {
			return true
		}
	}
	return false
}

// drive replays one unary call's stats events in the given order.
func drive(order []stats.RPCStats) *span.Span {
	leg := span.New("grpc_call", 0)
	p := &grpcPhases{anchor: time.Now(), leg: leg, start: 0}
	for _, e := range order {
		p.handle(e)
	}
	p.finish()
	return leg
}

func TestGRPCPhasesRecordsEveryPhaseInWireOrder(t *testing.T) {
	leg := drive([]stats.RPCStats{
		&stats.OutPayload{},
		&stats.InHeader{},
		&stats.InPayload{},
		&stats.InTrailer{},
	})
	for _, want := range []string{"ttfb", "transfer"} {
		if !has(leg, want) {
			t.Errorf("leg missing %q, has %v", want, phaseNames(leg))
		}
	}
}

func TestGRPCPhasesSurvivesTheAnswerBeatingTheSend(t *testing.T) {
	// InHeader before OutPayload: the exact order that left the leg holding
	// connect alone, because ttfb waited for a send it had not been told
	// about yet and transfer waited on ttfb.
	leg := drive([]stats.RPCStats{
		&stats.InHeader{},
		&stats.OutPayload{},
		&stats.InPayload{},
		&stats.InTrailer{},
	})
	for _, want := range []string{"ttfb", "transfer"} {
		if !has(leg, want) {
			t.Errorf("leg missing %q, has %v", want, phaseNames(leg))
		}
	}
}

func TestGRPCPhasesRecordsTTFBOnceForATrailersOnlyResponse(t *testing.T) {
	// The shape of most errors: trailers, no headers, no payload.
	leg := drive([]stats.RPCStats{
		&stats.OutPayload{},
		&stats.InTrailer{},
	})
	if !has(leg, "ttfb") {
		t.Errorf("a trailers-only response should still record ttfb, has %v", phaseNames(leg))
	}
	if has(leg, "transfer") {
		t.Errorf("nothing was transferred, so no transfer span belongs: %v", phaseNames(leg))
	}
}

func TestGRPCPhasesIgnoresEventsAfterFinish(t *testing.T) {
	// finish freezes the tree as Invoke returns; the pool folds and stores it
	// concurrently, so a late callback must not touch it.
	leg := span.New("grpc_call", 0)
	p := &grpcPhases{anchor: time.Now(), leg: leg}
	p.handle(&stats.OutPayload{})
	p.finish()
	p.handle(&stats.InHeader{})
	p.handle(&stats.InPayload{})
	if len(leg.Children) != 0 {
		t.Errorf("events after finish should be dropped, got %v", phaseNames(leg))
	}
}
