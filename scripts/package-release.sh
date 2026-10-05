#!/bin/sh
exec go run ./internal/buildinfo/cmd/release-artifact package "$@"
