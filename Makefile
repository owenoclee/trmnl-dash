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

# k3s image delivery (see k8s/README.md). Set DEPLOY_HOST and REGISTRY_CIP in
# .env, or override: make deploy DEPLOY_HOST=user@node REGISTRY_CIP=10.x.x.x:5000
DEPLOY_HOST  ?= user@your-k3s-node
REGISTRY_CIP ?= REGISTRY-CLUSTERIP:5000
IMAGE        := registry.local/trmnl:latest

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
	rm -f $(OUT) $(VIEWER) $(SERVER) $(SERVER_DEV)

# Build the arm64 image and ship it into the cluster's registry, then roll the
# Flux-managed deployment. The registry is ClusterIP-only, so the push goes via
# the node: import into its containerd, then push to the registry it can reach.
deploy:
	docker build -t $(IMAGE) .
	docker save $(IMAGE) | gzip | ssh $(DEPLOY_HOST) 'gunzip | sudo k3s ctr -n k8s.io images import -'
	ssh $(DEPLOY_HOST) 'sudo k3s ctr -n k8s.io images tag --force $(IMAGE) $(REGISTRY_CIP)/trmnl:latest \
		&& sudo k3s ctr -n k8s.io images push --plain-http $(REGISTRY_CIP)/trmnl:latest'
	ssh $(DEPLOY_HOST) 'sudo k3s kubectl -n trmnl rollout restart deploy/trmnl \
		&& sudo k3s kubectl -n trmnl rollout status deploy/trmnl'
