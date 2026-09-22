package main

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"example.com/rich/jobs"
	"example.com/rich/pb"
	"example.com/rich/svc"
)

func main() {
	root := &cobra.Command{Use: "rich [flags]", Run: func(cmd *cobra.Command, args []string) { serve() }}
	root.AddCommand(&cobra.Command{Use: "migrate", RunE: runMigrate})
	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}

func serve() {
	s := svc.NewServer()
	var r chi.Router = chi.NewRouter()
	r.Get("/ping", svc.Ping)
	r.Post("/orders", s.List)
	r.Method("DELETE", "/orders/{id}", http.HandlerFunc(s.List))
	g := grpc.NewServer()
	pb.RegisterOrderServiceServer(g, s)
	jobs.Register("refund", jobs.ProcessRefund)
	log.Fatal(http.ListenAndServe(":8080", r))
}

func runMigrate(cmd *cobra.Command, args []string) error { return nil }
