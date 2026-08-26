package main

import (
	"encoding/json"
	"flag"
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

func (w *jsonEventWriter) WriteReady(ready Ready) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.encoder.Encode(ready)
}

func main() {
	log.SetFlags(0)
	host := flag.String("host", defaultHost, "listener address (must remain 127.0.0.1)")
	port := flag.Int("port", 43123, "TCP port (0 chooses an ephemeral test port)")
	origin := flag.String("origin", "https://app.darkfactory.build", "exact browser Origin to accept")
	path := flag.String("path", defaultPath, "exact WebSocket request path")
	flag.Parse()

	output := &jsonEventWriter{encoder: json.NewEncoder(os.Stdout)}
	server, err := NewServer(Config{
		BindHost:       *host,
		Port:           *port,
		ExpectedOrigin: *origin,
		Path:           *path,
		EventWriter:    output.Write,
		ReadyWriter:    output.WriteReady,
	})
	if err != nil {
		log.Fatal(err)
	}
	_, err = server.Start()
	if err != nil {
		log.Fatal(err)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	if err := server.Close(); err != nil {
		log.Print(err)
	}
}
