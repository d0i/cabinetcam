.PHONY: build clean test mock-ollama client client-mac client-mac-arm

build:
	go build -o cabinetcam ./cmd/srv

clean:
	rm -f cabinetcam
	rm -f tools/mock-ollama/mock-ollama
	rm -f tools/annotate-client/annotate-client*

test:
	go test ./...

mock-ollama:
	go build -o tools/mock-ollama/mock-ollama ./tools/mock-ollama

client:
	go build -o tools/annotate-client/annotate-client ./tools/annotate-client

# Cross-compile annotation client for macOS
client-mac: client-mac-arm client-mac-amd64

client-mac-arm:
	GOOS=darwin GOARCH=arm64 go build -o tools/annotate-client/annotate-client-darwin-arm64 ./tools/annotate-client

client-mac-amd64:
	GOOS=darwin GOARCH=amd64 go build -o tools/annotate-client/annotate-client-darwin-amd64 ./tools/annotate-client
