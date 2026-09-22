// Package svc is the service: gRPC methods, HTTP handlers, closures and
// method values.
package svc

import (
	"context"
	"net/http"
	"strings"

	"example.com/rich/cache"
	"example.com/rich/pb"
)

// Server implements the gRPC service and serves HTTP.
type Server struct {
	pb.UnimplementedOrderServiceServer
	cache *cache.Cache[string, int]
}

// NewServer wires the cache.
func NewServer() *Server {
	return &Server{cache: cache.New[string, int]()}
}

// Create is the implemented gRPC method.
func (s *Server) Create(ctx context.Context, req *pb.CreateReq) (*pb.CreateResp, error) {
	s.cache.Put(req.Name, len(req.Name))
	lengths := cache.Map([]string{req.Name, req.Name + "x"}, s.helper)
	upper := func(v string) string { return normalize(strings.ToUpper(v)) }
	_ = upper(req.Name)
	return &pb.CreateResp{ID: req.Name + string(rune(lengths[0]))}, nil
}

// helper is passed as a method value to Map.
func (s *Server) helper(v string) int { return len(v) + 1 }

// normalize is called only from a closure inside Create.
func normalize(v string) string { return strings.TrimSpace(v) }

// List serves GET/POST /orders.
func (s *Server) List(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cache.Get(r.URL.Path); ok {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

// Ping is a plain handler function.
func Ping(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("pong"))
}
