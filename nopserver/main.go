package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/gcinterceptor/gci-go/httphandler"
)

var (
	port      = flag.Int("port", 8080, "Server port to listen")
	useGCI    = flag.Bool("gci", false, "Whether to use GCI.")
	nopHandle = func(http.ResponseWriter, *http.Request) {}
)

func main() {
	flag.Parse()
	if *useGCI {
		http.Handle("/", httphandler.GCI(nopHandle))
	} else {
		http.HandleFunc("/", nopHandle)
	}
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
}
