// Package grpcstub serves a .proto with handlers that speak JSON.
//
// It exists because a gRPC server normally needs generated code, and generated
// code needs protoc in everyone's path and a checked-in build step. The engine
// already compiles .proto files at run time to make the calls (ADR 0018); this
// turns the same descriptors around and serves them, so a stub is a schema plus
// a few functions and nothing has to be generated.
//
// Handlers take and return JSON because that is the shape the rest of the
// project is written in — a flow's `message:` block, a captured payload, a
// JSONPath assertion — so a stub reads like the flow that calls it.
package grpcstub

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Handler answers one unary call. request is the decoded message as JSON; the
// returned JSON is encoded into the method's output type.
//
// Returning a status error (status.Error(codes.ResourceExhausted, ...)) sends
// that status, which is the point of most stubs here. Handlers can also call
// grpc.SetHeader and grpc.SetTrailer on ctx to send metadata.
type Handler func(ctx context.Context, request []byte) ([]byte, error)

// Server is a gRPC server for one .proto.
type Server struct {
	files    linker.Files
	srv      *grpc.Server
	handlers map[string]Handler
}

// New compiles protoPath and prepares a server for the services it declares.
// Handlers are registered before Serve; a method with no handler answers
// UNIMPLEMENTED, exactly as a real server would.
func New(protoPath string, importPaths ...string) (*Server, error) {
	imports := append([]string{filepath.Dir(protoPath)}, importPaths...)
	compiler := protocompile.Compiler{
		Resolver:       protocompile.WithStandardImports(&protocompile.SourceResolver{ImportPaths: imports}),
		SourceInfoMode: protocompile.SourceInfoNone,
	}
	files, err := compiler.Compile(context.Background(), filepath.Base(protoPath))
	if err != nil {
		return nil, fmt.Errorf("compiling %s: %w", protoPath, err)
	}
	return &Server{files: files, handlers: map[string]Handler{}}, nil
}

// Handle registers the handler for `package.Service/Method`.
func (s *Server) Handle(method string, h Handler) {
	s.handlers[strings.TrimPrefix(method, "/")] = h
}

// Serve builds the service descriptors from the schema and serves until Stop.
func (s *Server) Serve(ln net.Listener) error {
	s.srv = grpc.NewServer()
	for _, fd := range s.files {
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			s.register(services.Get(i))
		}
	}
	return s.srv.Serve(ln)
}

func (s *Server) Stop() {
	if s.srv != nil {
		s.srv.Stop()
	}
}

// register turns one service descriptor into a grpc.ServiceDesc whose handlers
// decode into dynamic messages. The nil implementation is deliberate:
// RegisterService only type-checks an implementation that is actually there,
// and every method here is closed over instead.
func (s *Server) register(sd protoreflect.ServiceDescriptor) {
	desc := &grpc.ServiceDesc{ServiceName: string(sd.FullName()), HandlerType: (*any)(nil)}
	methods := sd.Methods()
	for i := 0; i < methods.Len(); i++ {
		md := methods.Get(i)
		if md.IsStreamingClient() || md.IsStreamingServer() {
			continue
		}
		full := fmt.Sprintf("%s/%s", sd.FullName(), md.Name())
		desc.Methods = append(desc.Methods, grpc.MethodDesc{
			MethodName: string(md.Name()),
			Handler:    s.dispatch(full, md),
		})
	}
	s.srv.RegisterService(desc, nil)
}

func (s *Server) dispatch(full string, md protoreflect.MethodDescriptor) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		h, ok := s.handlers[full]
		if !ok {
			return nil, status.Errorf(codes.Unimplemented, "%s has no handler in this stub", full)
		}

		in := dynamicpb.NewMessage(md.Input())
		if err := dec(in); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "decoding request: %v", err)
		}
		reqJSON, err := protojson.Marshal(in)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "rendering request as JSON: %v", err)
		}

		respJSON, err := h(ctx, reqJSON)
		if err != nil {
			return nil, err
		}

		out := dynamicpb.NewMessage(md.Output())
		if len(respJSON) > 0 {
			if err := protojson.Unmarshal(respJSON, out); err != nil {
				return nil, status.Errorf(codes.Internal, "handler answer does not fit %s: %v", md.Output().FullName(), err)
			}
		}
		return proto.Message(out), nil
	}
}
