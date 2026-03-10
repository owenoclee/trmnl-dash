# TRMNL ePaper Dashboard - Build System
# Target: 1872x1404 PNG @ 227 PPI (10.3" display)

PPI    := 227
SRC    := dashboard.typ
OUT    := dashboard.png
VIEWER := .viewer

.PHONY: build preview open clean

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

clean:
	rm -f $(OUT) $(VIEWER)
