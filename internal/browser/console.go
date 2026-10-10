package browser

import (
	"bytes"
	"compress/gzip"
	"embed"
	"net/http"
)

// ConsolePath serves this build's signed console bundle to an allowed origin.
const ConsolePath = "/console.js"

//go:embed console
var consoleFiles embed.FS

// Console is this build's console bundle gzipped, or nil for a build made
// without one (see console/README.md).
func Console() []byte {
	source, err := consoleFiles.ReadFile("console/console.js")
	if err != nil {
		return nil
	}
	var compressed bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	_, _ = writer.Write(source)
	_ = writer.Close()
	return compressed.Bytes()
}

// handleConsole answers with the signed bundle (relayhost.SignConsole) only
// for this listener's exact Host and an allowlisted Origin, which it echoes.
// The signature, not this check, is what stops another process that holds
// the port from running code in the app origin.
func (server *Server) handleConsole(writer http.ResponseWriter, request *http.Request) {
	origin, ok := server.validRequest(request)
	if !ok || request.Method != http.MethodGet || len(server.console) == 0 {
		http.Error(writer, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	writer.Header().Set("Access-Control-Allow-Origin", origin)
	writer.Header().Set("Vary", "Origin")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/octet-stream")
	_, _ = writer.Write(server.console)
}
