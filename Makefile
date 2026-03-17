# TRMNL ePaper Dashboard - Build System
# Target: 1872x1404 PNG @ 227 PPI (10.3" display)
# Stack: HTML/SVG template → headless Chrome → PNG

# Load .env if present (copy .env.example to .env and fill in your values)
-include .env
export

TMPL   := dashboard.html
OUT    := dashboard.png
VIEWER := .viewer
PORT   ?= 8080

SERVER := server

.PHONY: preview open clean server serve

$(VIEWER): viewer.swift
	swiftc -o $(VIEWER) viewer.swift

# Live preview: starts the server in dev mode (no device auth), opens the
# e-ink viewer, and re-renders on every save. Requires: brew install fswatch
# Zoom is auto-detected from the screen; override with ZOOM=56 if needed.
# Pass DEBUG=1 to show margin guides.
preview: $(SERVER) $(VIEWER)
	@DEV=1 ./$(SERVER) --addr :$(PORT) --tmpl $(TMPL) --out $(OUT) & \
	 trap "kill 0 2>/dev/null" EXIT INT TERM; \
	 echo "Waiting for first render…"; \
	 until curl -sf http://localhost:$(PORT)/api/display >/dev/null 2>&1; do sleep 0.1; done; \
	 fswatch -o $(TMPL) | while read -r _; do \
	   curl -sf http://localhost:$(PORT)/api/display >/dev/null; \
	 done & \
	 ./$(VIEWER) $(OUT) $(ZOOM)

open: $(SERVER)
	@DEV=1 ./$(SERVER) --addr :$(PORT) --tmpl $(TMPL) --out $(OUT) & \
	 trap "kill 0 2>/dev/null" EXIT; \
	 until curl -sf http://localhost:$(PORT)/api/display >/dev/null 2>&1; do sleep 0.1; done; \
	 open $(OUT)

$(SERVER): server.go go.mod go.sum
	go build -o $(SERVER) .

# Build the server binary without running it
server: $(SERVER)

# Render PNG and start the BYOS server.
# Optional: make serve ADDR=:9090 REFRESH_RATE=900
serve: $(SERVER)
	./$(SERVER) \
		$(if $(ADDR),--addr $(ADDR),) \
		$(if $(REFRESH_RATE),--refresh-rate $(REFRESH_RATE),)

clean:
	rm -f $(OUT) $(VIEWER) $(SERVER) $(SERVER_DEV)
