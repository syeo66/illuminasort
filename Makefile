.PHONY: all build test install clean

BINARY_NAME=illuminasort
INSTALL_DIR=$(HOME)/bin

all: build

build:
	go build -o $(BINARY_NAME)

test:
	go test ./...

install: build
	mkdir -p $(INSTALL_DIR)
	cp $(BINARY_NAME) $(INSTALL_DIR)/
	chmod +x $(INSTALL_DIR)/$(BINARY_NAME)
	@echo "Installed $(BINARY_NAME) to $(INSTALL_DIR)/"

clean:
	rm -f $(BINARY_NAME)
	@echo "Cleaned build artifacts"
