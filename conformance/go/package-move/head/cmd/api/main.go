package main

import (
	"database/sql"
	"log"
	"net/http"

	"example.com/fx/db"
	"example.com/fx/ordering"
)

func main() {
	conn, err := sql.Open("postgres", "")
	if err != nil {
		log.Fatal(err)
	}
	store := db.NewSQLStore(conn)
	svc := ordering.New(store)
	h := ordering.NewHandler(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", h.Create)
	log.Fatal(http.ListenAndServe(":8080", mux))
}
