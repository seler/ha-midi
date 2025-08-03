#!/bin/bash

# Diagnostic script for ha-midi on Raspberry Pi

echo "=== Raspberry Pi ha-midi Diagnostics ==="
echo
echo "System Information:"
echo "==================="
echo "Hostname: $(hostname)"
echo "Architecture: $(uname -m)"
echo "Kernel: $(uname -r)"
echo "OS: $(lsb_release -d 2>/dev/null | cut -f2 || cat /etc/os-release | grep PRETTY_NAME | cut -d'"' -f2)"
echo

echo "Checking /usr/local/bin/ha-midi:"
echo "================================="
if [ -f /usr/local/bin/ha-midi ]; then
    echo "✓ File exists"
    echo
    echo "File details:"
    ls -la /usr/local/bin/ha-midi
    echo
    echo "File type:"
    file /usr/local/bin/ha-midi
    echo
    echo "ELF details (if binary):"
    readelf -h /usr/local/bin/ha-midi 2>/dev/null | grep -E "Class:|Machine:" || echo "Not an ELF binary"
    echo
    echo "Dynamic linker:"
    readelf -l /usr/local/bin/ha-midi 2>/dev/null | grep "interpreter:" || echo "No interpreter found"
    echo
    echo "Dynamic dependencies:"
    ldd /usr/local/bin/ha-midi 2>&1 || echo "Cannot read dependencies"
    echo
    # Check for musl vs glibc
    if file /usr/local/bin/ha-midi | grep -q "musl"; then
        echo "⚠️  WARNING: Binary is linked against musl libc (Alpine Linux)"
        echo "   This won't work on Raspberry Pi OS which uses glibc."
        echo "   Download the latest release which uses glibc."
    fi
else
    echo "✗ File does not exist at /usr/local/bin/ha-midi"
fi

echo
echo "Checking service configuration:"
echo "================================"
if [ -f /etc/systemd/system/ha-midi.service ]; then
    echo "✓ Service file exists at /etc/systemd/system/ha-midi.service"
    echo "ExecStart line:"
    grep ExecStart /etc/systemd/system/ha-midi.service
elif [ -f /lib/systemd/system/ha-midi.service ]; then
    echo "✓ Service file exists at /lib/systemd/system/ha-midi.service"
    echo "ExecStart line:"
    grep ExecStart /lib/systemd/system/ha-midi.service
else
    echo "✗ Service file not found"
fi

echo
echo "Configuration file:"
echo "==================="
if [ -f /etc/ha-midi/config.yaml ]; then
    echo "✓ Config exists at /etc/ha-midi/config.yaml"
    echo "File size: $(ls -lh /etc/ha-midi/config.yaml | awk '{print $5}')"
else
    echo "✗ Config not found at /etc/ha-midi/config.yaml"
fi

echo
echo "Recent service logs:"
echo "===================="
sudo journalctl -u ha-midi -n 20 --no-pager 2>/dev/null || echo "No logs available"

echo
echo "Recommended binary for your Pi:"
echo "================================"
ARCH=$(uname -m)
if [[ "$ARCH" == "aarch64" ]] || [[ "$ARCH" == "arm64" ]]; then
    echo "→ You should use: ha-midi_linux_arm64"
    echo "  Download URL: https://github.com/seler/ha-midi/releases/latest/download/ha-midi_linux_arm64"
elif [[ "$ARCH" == "armv7l" ]] || [[ "$ARCH" == "armhf" ]]; then
    echo "→ You should use: ha-midi_linux_armv7"
    echo "  Download URL: https://github.com/seler/ha-midi/releases/latest/download/ha-midi_linux_armv7"
elif [[ "$ARCH" == "armv6l" ]]; then
    echo "→ Your Pi has ARMv6 architecture (older model)"
    echo "  Unfortunately, we don't build for ARMv6. Consider upgrading to a newer Pi."
else
    echo "→ Unknown architecture: $ARCH"
fi
