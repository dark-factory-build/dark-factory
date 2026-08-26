package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

type jsonEventWriter struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func (w *jsonEventWriter) Write(event ProbeEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.encoder.Encode(event)
}

func main() {
	log.SetFlags(0)
	host := flag.String("host", defaultHost, "listener address (must remain 127.0.0.1)")
	port := flag.Int("port", 43123, "TCP port (0 chooses an ephemeral test port)")
	origin := flag.String("origin", "https://app.darkfactory.build", "exact browser Origin to accept")
	path := flag.String("path", defaultPath, "exact WebSocket request path")
	flag.Parse()

	server, err := NewServer(Config{
		BindHost:       *host,
		Port:           *port,
		ExpectedOrigin: *origin,
		Path:           *path,
		EventWriter:    (&jsonEventWriter{encoder: json.NewEncoder(os.Stdout)}).Write,
	})
	if err != nil {
		log.Fatal(err)
	}
	ready, err := server.Start()
	if err != nil {
		log.Fatal(err)
	}
	encoded, err := json.Marshal(ready)
	if err != nil {
		log.Fatal(err)
	}
	// This is the only readiness line. It is intentionally machine-readable so
	// a preview script can capture the exact URL and policy it must exercise.
	fmt.Println(string(encoded))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	if err := server.Close(); err != nil {
		log.Print(err)
	}
}
