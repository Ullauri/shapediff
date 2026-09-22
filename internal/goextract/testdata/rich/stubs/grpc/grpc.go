// Package grpc is a stub of the server registration surface.
package grpc

type ServiceRegistrar interface{ RegisterService(name string, impl any) }

type Server struct{ services map[string]any }

func NewServer() *Server { return &Server{services: map[string]any{}} }

func (s *Server) RegisterService(name string, impl any) { s.services[name] = impl }
