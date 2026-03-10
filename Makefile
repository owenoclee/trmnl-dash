# TRMNL ePaper Dashboard - Build System
# Target: 1872x1404 PNG @ 227 PPI (10.3" display)

PPI    := 227
SRC    := dashboard.typ
OUT    := dashboard.png
VIEWER := .viewer

SERVER := server

.PHONY: build preview open clean server serve

build:
	typst compile --format png --ppi $(PPI) $(SRC) $(OUT)

$(VIEWER): viewer.swift
	swiftc -o $(VIEWER) viewer.swift

# Zoom is auto-detected from the screen at runtime.
# Override if needed: make preview ZOOM=50
preview: build $(VIEWER)
	./$(VIEWER) $(OUT) $(ZOOM)

open: build
	open $(OUT)

$(SERVER): server.go go.mod
	go build -o $(SERVER) .

# Build the server binary without running it
server: $(SERVER)

# Build the PNG and start the BYOS server.
# Optional: make serve ADDR=:9090 REFRESH_RATE=900
serve: build $(SERVER)
	./$(SERVER) \
		$(if $(ADDR),--addr $(ADDR),) \
		$(if $(REFRESH_RATE),--refresh-rate $(REFRESH_RATE),)

clean:
	rm -f $(OUT) $(VIEWER) $(SERVER)
