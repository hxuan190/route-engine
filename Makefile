.PHONY: build build-hft test clean vendor run run-hft install-service uninstall-service

# Go configuration
GO_BUILD_FLAGS := -v
BINARY := hyperion-api
INSTALL_DIR := /opt/hyperion-api

# Standard build
build:
	go build $(GO_BUILD_FLAGS) ./...

# HFT optimized build (stripped, no CGO)
build-hft:
	@echo "Building HFT-optimized binary..."
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY) ./cmd/runtime/main.go
	@echo "Built: $(BINARY) ($$(du -h $(BINARY) | cut -f1))"

test:
	go test ./...

clean:
	go clean
	rm -f $(BINARY)

vendor:
	go mod tidy
	go mod vendor

# Standard run
run:
	./run.sh

# HFT optimized run (with CPU pinning, huge pages)
run-hft:
	./deploy/run-hft.sh

# Install systemd service (requires sudo)
install-service: build-hft
	@echo "Installing Hyperion API service..."
	sudo mkdir -p $(INSTALL_DIR)/logs $(INSTALL_DIR)/data
	sudo cp $(BINARY) $(INSTALL_DIR)/
	sudo cp .env $(INSTALL_DIR)/ 2>/dev/null || true
	sudo cp deploy/hyperion-api.service /etc/systemd/system/
	sudo useradd -r -s /bin/false hyperion 2>/dev/null || true
	sudo chown -R hyperion:hyperion $(INSTALL_DIR)
	sudo systemctl daemon-reload
	sudo systemctl enable hyperion-api
	@echo "Service installed. Start with: sudo systemctl start hyperion-api"

# Uninstall systemd service
uninstall-service:
	@echo "Uninstalling Hyperion API service..."
	sudo systemctl stop hyperion-api 2>/dev/null || true
	sudo systemctl disable hyperion-api 2>/dev/null || true
	sudo rm -f /etc/systemd/system/hyperion-api.service
	sudo systemctl daemon-reload
	@echo "Service uninstalled. Data remains in $(INSTALL_DIR)"

# Show service status
status:
	@systemctl status hyperion-api || true

# Show service logs
logs:
	@journalctl -u hyperion-api -f

# Quick system tune (requires sudo)
tune:
	@echo "Applying HFT system tunings..."
	@sudo sh -c 'echo 1024 > /proc/sys/vm/nr_hugepages' || true
	@for cpu in /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor; do \
		sudo sh -c "echo performance > $$cpu" 2>/dev/null || true; \
	done
	@echo "System tuned. See deploy/TUNING.md for permanent settings."
