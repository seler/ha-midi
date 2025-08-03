#!/bin/bash

# Raspberry Pi installation script for ha-midi

set -e

echo "=== Raspberry Pi ha-midi Installation Script ==="
echo

# Detect architecture
ARCH=$(uname -m)
echo "Detected architecture: $ARCH"

# Determine the correct binary
BINARY_SUFFIX=""
if [[ "$ARCH" == "aarch64" ]] || [[ "$ARCH" == "arm64" ]]; then
    BINARY_SUFFIX="linux_arm64"
    echo "→ Using ARM64 binary"
elif [[ "$ARCH" == "armv7l" ]] || [[ "$ARCH" == "armhf" ]]; then
    BINARY_SUFFIX="linux_armv7"
    echo "→ Using ARMv7 binary"
elif [[ "$ARCH" == "armv6l" ]]; then
    echo "ERROR: ARMv6 is not supported. Please upgrade to a newer Raspberry Pi."
    exit 1
else
    echo "ERROR: Unknown architecture: $ARCH"
    exit 1
fi

# Check if file exists and its details
if [ -f /usr/local/bin/ha-midi ]; then
    echo
    echo "Existing binary found at /usr/local/bin/ha-midi"
    echo "File details:"
    file /usr/local/bin/ha-midi || true
    echo "Size: $(ls -lh /usr/local/bin/ha-midi | awk '{print $5}')"
    echo
    echo "This appears to be the wrong architecture. Removing..."
    sudo rm -f /usr/local/bin/ha-midi
fi

# Download the correct binary
RELEASE_URL="https://github.com/seler/ha-midi/releases/latest/download/ha-midi_${BINARY_SUFFIX}"
echo
echo "Downloading from: $RELEASE_URL"
echo

# Create temp directory
TEMP_DIR=$(mktemp -d)
cd "$TEMP_DIR"

# Download with curl or wget
if command -v curl >/dev/null 2>&1; then
    curl -L -o ha-midi "$RELEASE_URL"
elif command -v wget >/dev/null 2>&1; then
    wget -O ha-midi "$RELEASE_URL"
else
    echo "ERROR: Neither curl nor wget found. Please install one:"
    echo "  sudo apt-get update && sudo apt-get install -y curl"
    exit 1
fi

# Check if download was successful
if [ ! -f ha-midi ]; then
    echo "ERROR: Download failed"
    exit 1
fi

# Check file size (should be > 1MB for a real binary)
SIZE=$(stat -c%s ha-midi)
if [ "$SIZE" -lt 1000000 ]; then
    echo "ERROR: Downloaded file is too small (${SIZE} bytes). It might be an error page."
    echo "File content preview:"
    head -n 5 ha-midi
    exit 1
fi

echo "Download successful ($(ls -lh ha-midi | awk '{print $5}'))"

# Make executable and move to /usr/local/bin
chmod +x ha-midi
sudo mv ha-midi /usr/local/bin/ha-midi

echo
echo "✓ Binary installed to /usr/local/bin/ha-midi"

# Verify installation
echo
echo "Verifying installation..."
file /usr/local/bin/ha-midi

# Test if it can run (just show version/help)
echo
echo "Testing binary (this should show usage info):"
/usr/local/bin/ha-midi --help 2>&1 || true

# Check for config file
echo
if [ -f /etc/ha-midi/config.yaml ]; then
    echo "✓ Config file found at /etc/ha-midi/config.yaml"
else
    echo "⚠ Config file not found at /etc/ha-midi/config.yaml"
    echo "  Create it from the example:"
    echo "  sudo mkdir -p /etc/ha-midi"
    echo "  sudo curl -L -o /etc/ha-midi/config.example.yaml https://raw.githubusercontent.com/seler/ha-midi/master/config.example.yaml"
    echo "  sudo cp /etc/ha-midi/config.example.yaml /etc/ha-midi/config.yaml"
    echo "  sudo nano /etc/ha-midi/config.yaml  # Edit with your settings"
fi

# Service management
echo
echo "=== Service Management ==="
if systemctl is-active --quiet ha-midi; then
    echo "Service is currently running. Restarting..."
    sudo systemctl restart ha-midi
    sleep 2
    sudo systemctl status ha-midi --no-pager | head -n 10
else
    echo "Service is not running."
    echo "To start the service:"
    echo "  sudo systemctl start ha-midi"
    echo "  sudo systemctl status ha-midi"
fi

echo
echo "=== Installation Complete ==="
echo
echo "Commands:"
echo "  Start service:   sudo systemctl start ha-midi"
echo "  Stop service:    sudo systemctl stop ha-midi"
echo "  Restart service: sudo systemctl restart ha-midi"
echo "  View logs:       sudo journalctl -u ha-midi -f"
echo "  Check status:    sudo systemctl status ha-midi"

# Cleanup
cd /
rm -rf "$TEMP_DIR"
