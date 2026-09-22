package main

import (
	"database/sql"
	"log"
	"net/http"

	"example.com/fx/db"
	"example.com/fx/orders"
)

func main() {
	conn, err := sql.Open("postgres", "")
	if err != nil {
		log.Fatal(err)
	}
	store := db.NewSQLStore(conn)
	svc := orders.New(store)
	h := orders.NewHandler(svc)
	mux := http.NewServeMux()
	_ = h
	log.Fatal(http.ListenAndServe(":8080", mux))
}
