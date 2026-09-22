package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"

	"example.com/fx/db"
	"example.com/fx/orders"
)

func main() {
	conn, err := sql.Open("postgres", "")
	if err != nil {
		log.Fatal(err)
	}
	var store orders.Store = db.NewSQLStore(conn)
	if len(os.Args) > 1 {
		store = &db.MemStore{}
	}
	svc := orders.New(store)
	h := orders.NewHandler(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", h.Create)
	log.Fatal(http.ListenAndServe(":8080", mux))
}
