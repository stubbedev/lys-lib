module github.com/stubbedev/lys-lib

go 1.27.3

require (
	github.com/ebitengine/purego v0.11.2
	github.com/go-text/typesetting v0.3.5
	github.com/kettek/apng v0.0.0-20250827064933-2bb5f5fcf253
	github.com/rivo/uniseg v0.4.7
	github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef
	github.com/stubbedev/memory-go v0.3.0
	golang.org/x/image v0.47.0
	golang.org/x/net v0.62.0
	golang.org/x/sys v0.49.0
	golang.org/x/text v0.43.0
)

require modernc.org/memory v1.12.1 // indirect

tool github.com/stubbedev/lys-lib/third_party/wayland/cmd/wayland-scanner
