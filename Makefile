BINARY  = pocket-radio
PKG     = ./cmd/pocket-radio
GO     ?= go

.PHONY: help build run run_upnext run_kcrw run-debug run_upnext-debug run_kcrw-debug test test-integration vet clean

.DEFAULT_GOAL := help

# Show this help.
help:
	@echo "PocketRadio Console — make targets"
	@echo ""
	@echo "  build             Build the $(BINARY) binary"
	@echo "  run               Build and launch the full TUI"
	@echo "  run_upnext        Build and play the top of Up Next (mini mode)"
	@echo "  run_kcrw          Build and play KCRW (mini mode)"
	@echo "  run-debug         Build and launch full TUI with debug logging (debug.log)"
	@echo "  run_upnext-debug  Build and play Up Next with debug logging"
	@echo "  run_kcrw-debug    Build and play KCRW with debug logging"
	@echo ""
	@echo "  test              Run the hermetic test suite"
	@echo "  test-integration  Run tests including the real-mpv integration test"
	@echo "  vet               Run go vet"
	@echo "  clean             Remove the built binary"
	@echo ""
	@echo "  help              Show this help"
	@echo ""
	@echo "Requires mpv on PATH (brew install mpv)."

build:
	$(GO) build -o $(BINARY) $(PKG)

run: build
	./$(BINARY)

run_upnext: build
	./$(BINARY) up_next

run_kcrw: build
	./$(BINARY) kcrw

run-debug: build
	./$(BINARY) --debug

run_upnext-debug: build
	./$(BINARY) up_next --debug

run_kcrw-debug: build
	./$(BINARY) kcrw --debug

test:
	$(GO) test ./...

test-integration:
	$(GO) test -tags integration ./...

vet:
	$(GO) vet ./...

clean:
	rm -f $(BINARY)
