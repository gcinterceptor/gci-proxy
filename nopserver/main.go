package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
)

var port = flag.Int("port", 8080, "Server port to listen")

func main() {
	flag.Parse()
	http.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
}
