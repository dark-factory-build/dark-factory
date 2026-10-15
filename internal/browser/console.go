package browser

import (
	"bytes"
	"compress/gzip"
	"embed"
	"net/http"
)

// ConsolePath serves this build's signed console bundle to this listener's
// own page or an allowed origin.
const ConsolePath = "/console.js"

// shell is the desktop console page served at "/": same-origin, so the
// browser asks no loopback permission. It skips the 32-byte key and 64-byte
// signature ahead of the gzipped bundle (relayhost.SignConsole); the daemon
// that signed it served this page, so there is no other key to check. Under
// launchd no other process can hold this origin (Config.Socket).
const shell = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Dark Factory</title><style>body{margin:0;background:#070a09}</style><div id="root"></div><script type="module">
const signed = await (await fetch("/console.js")).blob();
const source = await new Response(signed.slice(96).stream().pipeThrough(new DecompressionStream("gzip"))).blob();
const { mount } = await import(URL.createObjectURL(new Blob([source], { type: "text/javascript" })));
mount(document.getElementById("root"), "factory", { browserPort: Number(location.port) });
</script>`

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

// pageRequest admits a GET on this listener's exact Host carrying either an
// allowlisted Origin, which it returns, or none with the Fetch Metadata a
// browser sends for its own same-origin load or an opened link.
func (server *Server) pageRequest(request *http.Request) (string, bool) {
	if request.Method != http.MethodGet || len(server.console) == 0 {
		return "", false
	}
	if site := request.Header.Get("Sec-Fetch-Site"); len(request.Header.Values("Origin")) == 0 && request.Host == server.host {
		return "", site == "same-origin" || site == "none"
	}
	return server.validRequest(request)
}

// handleShell serves the desktop console page at "/".
func (server *Server) handleShell(writer http.ResponseWriter, request *http.Request) {
	if _, ok := server.pageRequest(request); !ok {
		http.Error(writer, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	writer.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write([]byte(shell))
}

// handleConsole answers with the signed bundle (relayhost.SignConsole).
func (server *Server) handleConsole(writer http.ResponseWriter, request *http.Request) {
	origin, ok := server.pageRequest(request)
	if !ok {
		http.Error(writer, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	if origin != "" {
		writer.Header().Set("Access-Control-Allow-Origin", origin)
	}
	writer.Header().Set("Vary", "Origin")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/octet-stream")
	_, _ = writer.Write(server.console)
}
