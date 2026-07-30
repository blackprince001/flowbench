package adapters

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/blackprince001/flowbench/internal/ir"
)

// GRPCMethod is one resolved unary method: the two message types it moves and
// the resolver that can interpret anything they reference.
type GRPCMethod struct {
	// Path is the fully-qualified method with its leading slash — literally the
	// HTTP/2 `:path` gRPC puts on the wire.
	Path string

	Input  protoreflect.MessageDescriptor
	Output protoreflect.MessageDescriptor

	// Resolver interprets Any and extensions inside those messages. Without it
	// protojson renders a `google.protobuf.Any` as an error rather than as the
	// message it wraps.
	Resolver linker.Resolver
}

// ProtoRegistry compiles .proto files to descriptors and hands out methods.
//
// Compilation happens once per file per run, not once per call: parsing and
// linking a schema is real work, and at 10k VUs doing it per iteration would
// measure the generator rather than the target. The zero benefit of laziness
// here is why Prepare exists — a broken proto or a method that is not in it is
// a pre-run error, in the same class as an unreachable host, rather than a
// failure that only shows up once the run is under way.
type ProtoRegistry struct {
	// root is the directory flow-relative proto paths resolve against — the
	// directory of the flow file, so a path in a flow reads the way it would in
	// an editor.
	root string

	mu       sync.Mutex
	compiled map[string]linker.Files
}

func NewProtoRegistry(root string) *ProtoRegistry {
	return &ProtoRegistry{root: root, compiled: map[string]linker.Files{}}
}

// Prepare resolves every gRPC method in the scenario, so a schema problem is
// reported before the first request rather than by every VU at once.
func (r *ProtoRegistry) Prepare(ctx context.Context, sc *ir.Scenario) error {
	var problems []string
	for _, f := range sc.Flows {
		for i := range f.Steps {
			st := &f.Steps[i]
			if st.GRPC == nil {
				continue
			}
			if _, err := r.Method(ctx, st.GRPC); err != nil {
				problems = append(problems, fmt.Sprintf("flow %q step %q: %v", f.Name, st.ID, err))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("gRPC schema: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Method resolves the spec's method, compiling its proto on first use.
func (r *ProtoRegistry) Method(ctx context.Context, spec *ir.GRPCSpec) (*GRPCMethod, error) {
	files, err := r.compile(ctx, spec)
	if err != nil {
		return nil, err
	}

	service, method, _ := strings.Cut(strings.TrimPrefix(spec.Method, "/"), "/")
	for _, fd := range files {
		svc := fd.Services().ByName(protoreflect.Name(lastSegment(service)))
		if svc == nil || string(svc.FullName()) != service {
			continue
		}
		md := svc.Methods().ByName(protoreflect.Name(method))
		if md == nil {
			return nil, fmt.Errorf("service %s has no method %q; %s defines %s",
				service, method, spec.Proto, listMethods(svc))
		}
		if md.IsStreamingClient() || md.IsStreamingServer() {
			// Settled, not pending: streaming is out of v1 (ADR 0019, spike
			// #29). The span model could carry a stream — the ws slice already
			// built session scope and match/skip — but a run reports iterations
			// and per-flow-run latency, and neither says anything about a
			// stream held open for minutes.
			return nil, fmt.Errorf("method %s is streaming; v1 calls unary methods only (ADR 0019). %s defines %s",
				spec.Method, spec.Proto, listMethods(svc))
		}
		return &GRPCMethod{
			Path:     spec.GRPCPath(),
			Input:    md.Input(),
			Output:   md.Output(),
			Resolver: files.AsResolver(),
		}, nil
	}
	return nil, fmt.Errorf("%s defines no service %s; it defines %s", spec.Proto, service, listServices(files))
}

// compile parses and links the spec's proto, memoized on everything that can
// change the result.
func (r *ProtoRegistry) compile(ctx context.Context, spec *ir.GRPCSpec) (linker.Files, error) {
	// The proto's own directory is always an import path, so a schema that
	// imports its neighbours works with nothing declared — which is the common
	// case, and the one worth making free.
	path := r.resolve(spec.Proto)
	imports := append([]string{filepath.Dir(path)}, r.resolveAll(spec.ImportPaths)...)
	key := strings.Join(append([]string{path}, imports...), "\x00")

	r.mu.Lock()
	defer r.mu.Unlock()
	if files, ok := r.compiled[key]; ok {
		return files, nil
	}

	compiler := protocompile.Compiler{
		Resolver:       protocompile.WithStandardImports(&protocompile.SourceResolver{ImportPaths: imports}),
		SourceInfoMode: protocompile.SourceInfoNone,
	}
	files, err := compiler.Compile(ctx, filepath.Base(path))
	if err != nil {
		return nil, fmt.Errorf("compiling %s: %w%s", spec.Proto, err, editionHint(err))
	}
	r.compiled[key] = files
	return files, nil
}

// resolve makes a flow-relative path absolute against the registry's root. An
// already-absolute path is left alone, so a shared schema outside the repo is
// nameable.
func (r *ProtoRegistry) resolve(path string) string {
	if filepath.IsAbs(path) || r.root == "" {
		return path
	}
	return filepath.Join(r.root, path)
}

func (r *ProtoRegistry) resolveAll(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, r.resolve(p))
	}
	return out
}

// SupportedProtoSyntax is what the bundled compiler accepts. It is a property
// of the pinned protocompile version, not of FlowBench, and it moves when that
// dependency does — which is exactly why a flow gets told rather than left to
// read a parse error about a keyword the compiler has never heard of.
const SupportedProtoSyntax = "proto2, proto3, and edition 2023"

// editionHint appends what we support to an error that is really about a
// schema written for a newer protobuf than the compiler we ship.
func editionHint(err error) string {
	if !strings.Contains(strings.ToLower(err.Error()), "edition") {
		return ""
	}
	return fmt.Sprintf(" (this build compiles %s)", SupportedProtoSyntax)
}

func lastSegment(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// listServices and listMethods turn "not found" into a usable error: a typo in
// a package or a method name is the likeliest cause, and the fix is visible
// only when the alternatives are on screen.
func listServices(files linker.Files) string {
	var names []string
	for _, fd := range files {
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			names = append(names, string(svcs.Get(i).FullName()))
		}
	}
	if len(names) == 0 {
		return "no services at all"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func listMethods(svc protoreflect.ServiceDescriptor) string {
	var names []string
	methods := svc.Methods()
	for i := 0; i < methods.Len(); i++ {
		names = append(names, string(methods.Get(i).Name()))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
