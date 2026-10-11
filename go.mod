module github.com/dark-factory-build/dark-factory

go 1.27.0

toolchain go1.27.2

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/coder/websocket v1.8.15
	github.com/ncruces/go-sqlite3 v0.35.3
	github.com/tetratelabs/wazero v1.12.0
	go.opentelemetry.io/proto/otlp v1.11.0
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/sys v0.47.0
	google.golang.org/protobuf v1.36.12
	rsc.io/qr v0.2.0
)

require (
	github.com/mibk/dupl v1.1.0 // indirect
	github.com/ncruces/go-sqlite3-wasm/v3 v3.2.35304 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	golang.org/x/mod v0.40.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/telemetry v0.0.0-20260811182544-a038080d80e5 // indirect
	golang.org/x/tools v0.49.0 // indirect
)

tool (
	github.com/mibk/dupl
	golang.org/x/tools/cmd/deadcode
)
