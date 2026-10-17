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

# k3s image delivery (see k8s/README.md). Set PUSH_IMAGE in .env, or override:
# make deploy PUSH_IMAGE=/path/to/push-image.sh
PUSH_IMAGE ?= ./push-image.sh
BUILDER    ?= ocibuilder
PLATFORM   ?= linux/arm64
OCI_DIR    := .oci

.PHONY: preview open clean server serve deploy

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
	 ./$(VIEWER) $(OUT) $(ZOOM) & \
	 fswatch -o $(TMPL) | while read -r _; do \
	   curl -sf http://localhost:$(PORT)/api/display >/dev/null && \
	   ./$(VIEWER) $(OUT) $(ZOOM); \
	 done

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
# Optional (flag = make var, also picked up from .env):
#   make serve ADDR=:9090 REFRESH_RATE=900 RENDER_INTERVAL=120 PHOTO_STRATEGY=alphabetical
serve: $(SERVER)
	./$(SERVER) \
		$(if $(ADDR),--addr $(ADDR),) \
		$(if $(REFRESH_RATE),--refresh-rate $(REFRESH_RATE),) \
		$(if $(RENDER_INTERVAL),--render-interval $(RENDER_INTERVAL),) \
		$(if $(PHOTO_STRATEGY),--photo-strategy $(PHOTO_STRATEGY),)

clean:
	rm -rf $(OUT) $(VIEWER) $(SERVER) $(SERVER_DEV) $(OCI_DIR)

# Build the image as an OCI layout, hand it to $(PUSH_IMAGE) <layout> trmnl:latest,
# then roll the deployment (imagePullPolicy: Always pulls the new latest). OCI
# rather than `docker save`, because OCI-only registries such as zot reject
# Docker manifest v2; --provenance=false keeps buildx from wrapping the image in
# an attestation index the registry won't accept as a tag.
deploy:
	rm -rf $(OCI_DIR) && mkdir -p $(OCI_DIR)
	docker buildx --builder $(BUILDER) build --platform $(PLATFORM) \
		--provenance=false --sbom=false --output type=oci,dest=$(OCI_DIR)/img.tar .
	tar -xf $(OCI_DIR)/img.tar -C $(OCI_DIR) && rm $(OCI_DIR)/img.tar
	$(PUSH_IMAGE) $(OCI_DIR) trmnl:latest
	kubectl -n trmnl rollout restart deploy/trmnl
	kubectl -n trmnl rollout status deploy/trmnl --timeout=5m
