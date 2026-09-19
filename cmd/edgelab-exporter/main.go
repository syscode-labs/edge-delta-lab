package main

import (
	"example.com/edge-delta-lab/internal/exporter"
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	socket := flag.String("socket", "/state/admin.sock", "admin unix socket")
	listen := flag.String("listen", "127.0.0.1:9109", "loopback/private listen address")
	role := flag.String("role", "hub", "hub or client")
	instance := flag.String("instance", "edgelab", "stable instance label")
	flag.Parse()
	ex := exporter.New(exporter.Config{Socket: *socket, Role: *role, Instance: *instance, Timeout: 2 * time.Second, MaxResponse: 1 << 20})
	log.Printf("edgelab-exporter listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, ex))
}
