.PHONY: build clean stop start restart test

build:
	go build -o cabinetcam ./cmd/srv

clean:
	rm -f cabinetcam

test:
	go test ./...
