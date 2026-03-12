# TRMNL ePaper Dashboard - Build System
# Target: 1872x1404 PNG @ 227 PPI (10.3" display)
# Stack: HTML/SVG template → headless Chrome → PNG

# Load .env if present (copy .env.example to .env and fill in your values)
-include .env
export

TMPL   := dashboard.html
OUT    := dashboard.png
VIEWER := .viewer

SERVER := server

.PHONY: preview preview-debug open clean server serve

$(VIEWER): viewer.swift
	swiftc -o $(VIEWER) viewer.swift

# Live preview: fetch real weather, render PNG, open in e-ink simulator.
# Zoom is auto-detected from the screen; override with ZOOM=56 if needed.
preview: $(SERVER) $(VIEWER)
	./$(SERVER) --once --tmpl $(TMPL) --out $(OUT)
	./$(VIEWER) $(OUT) $(ZOOM)

# Preview with margin guides visible
preview-debug: $(SERVER) $(VIEWER)
	./$(SERVER) --once --debug --tmpl $(TMPL) --out $(OUT)
	./$(VIEWER) $(OUT) $(ZOOM)

open: $(SERVER)
	./$(SERVER) --once --tmpl $(TMPL) --out $(OUT)
	open $(OUT)

$(SERVER): server.go go.mod
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
	rm -f $(OUT) $(VIEWER) $(SERVER)
