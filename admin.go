package main

import (
	"fmt"
	"log"
	"net/http"
)

func StartAdmin() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "goweb")
	})
	listen := "[::]:62022"
	go func() {
		log.Fatal(http.ListenAndServe(listen, nil))
	}()
	log.Println(fmt.Sprintf("Listening on http://%v/ (internal)", listen))
}
