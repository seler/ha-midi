# HA-MIDI Bridge

Bridge MIDI devices with Home Assistant via MQTT.

## Features

- **Auto-discovery** of USB MIDI devices
- **Bidirectional communication** between MIDI and Home Assistant
- **MQTT Discovery** for automatic entity creation
- **Multiple device support**
- **Smart value conversion** (pitch bend → 0-100%)

## Supported MIDI Messages

| MIDI Type | Home Assistant Entity | Range |
|-----------|----------------------|--------|
| Note On/Off | Binary sensor + Light | On/Off |
| Control Change | Number | 0-127 |
| Program Change | Number | 0-127 |
| Pitch Bend | Number | 0-100% |

## Prerequisites

- Home Assistant with MQTT broker configured
- USB MIDI device(s)
- Network access to MQTT broker

## Installation

### Linux (Systemd)

```bash
# Create directories and user
sudo mkdir -p /etc/ha-midi /var/log/ha-midi
sudo useradd -r -s /bin/false -G audio midi
sudo chown midi:audio /var/log/ha-midi

# Download config
sudo wget -O /etc/ha-midi/config.yaml \
  https://raw.githubusercontent.com/seler/ha-midi/main/config.example.yaml

# Edit config (set MQTT broker details)
sudo nano /etc/ha-midi/config.yaml

# Download binary directly (choose your architecture)
# AMD64:
sudo curl -L https://github.com/seler/ha-midi/releases/latest/download/ha-midi_Linux_x86_64 \
  -o /usr/local/bin/ha-midi
# ARM64 (Raspberry Pi 4):
# sudo curl -L https://github.com/seler/ha-midi/releases/latest/download/ha-midi_Linux_arm64 \
#   -o /usr/local/bin/ha-midi
# ARMv6 (Raspberry Pi Zero):
# sudo curl -L https://github.com/seler/ha-midi/releases/latest/download/ha-midi_Linux_armv6 \
#   -o /usr/local/bin/ha-midi

sudo chmod +x /usr/local/bin/ha-midi

# Install service
sudo wget -O /etc/systemd/system/ha-midi.service \
  https://raw.githubusercontent.com/seler/ha-midi/main/ha-midi.service

# Start service
sudo systemctl daemon-reload
sudo systemctl enable --now ha-midi
```

### Windows

```powershell
# 1. Create directory
New-Item -ItemType Directory -Path "C:\ha-midi" -Force

# 2. Download the Windows executable directly
Invoke-WebRequest -Uri "https://github.com/seler/ha-midi/releases/latest/download/ha-midi_Windows_x86_64.exe" `
  -OutFile "C:\ha-midi\ha-midi.exe"

# 3. Download config example
Invoke-WebRequest -Uri "https://raw.githubusercontent.com/seler/ha-midi/main/config.example.yaml" `
  -OutFile "C:\ha-midi\config.yaml"

# 4. Edit config.yaml with your MQTT settings

# 5. Run as administrator (for MIDI device access)
C:\ha-midi\ha-midi.exe

# Optional: Create Windows service using NSSM (https://nssm.cc/)
nssm install ha-midi "C:\ha-midi\ha-midi.exe"
nssm start ha-midi
```

### macOS

```bash
# 1. Download binary directly
# Intel Mac:
sudo curl -L https://github.com/seler/ha-midi/releases/latest/download/ha-midi_Darwin_x86_64 \
  -o /usr/local/bin/ha-midi
# Apple Silicon (M1/M2):
# sudo curl -L https://github.com/seler/ha-midi/releases/latest/download/ha-midi_Darwin_arm64 \
#   -o /usr/local/bin/ha-midi

sudo chmod +x /usr/local/bin/ha-midi

# 2. Create config directory
mkdir -p ~/.ha-midi

# 3. Download config
curl -o ~/.ha-midi/config.yaml \
  https://raw.githubusercontent.com/seler/ha-midi/main/config.example.yaml

# 4. Edit config
nano ~/.ha-midi/config.yaml

# 5. Run
ha-midi -config ~/.ha-midi/config.yaml

# Optional: Create LaunchAgent for auto-start
cat > ~/Library/LaunchAgents/com.ha-midi.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.ha-midi</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/ha-midi</string>
        <string>-config</string>
        <string>~/.ha-midi/config.yaml</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
EOF

launchctl load ~/Library/LaunchAgents/com.ha-midi.plist
```

## Configuration

Edit your `config.yaml`:

```yaml
mqtt:
  broker: "192.168.1.100"  # Your MQTT broker IP
  username: "mqtt_user"     # Optional
  password: "mqtt_pass"     # Optional

midi:
  auto_connect: true
  light_off_method: "multi"  # Works with most devices
```

## Home Assistant Automation

Example: Toggle light with MIDI button

```yaml
automation:
  - alias: "MIDI Button Toggle Light"
    trigger:
      - platform: state
        entity_id: binary_sensor.midi_device_note_60
        to: "on"
    action:
      - service: light.toggle
        target:
          entity_id: light.living_room
```

## Troubleshooting

### Linux
```bash
sudo journalctl -u ha-midi -f
```

### Windows
```powershell
# Check Windows Event Viewer or run in console mode
C:\ha-midi\ha-midi.exe -debug
```

### macOS
```bash
# View logs
log show --predicate 'process == "ha-midi"' --last 1h

# Or run in debug mode
ha-midi -config ~/.ha-midi/config.yaml -debug
```

### Enable Debug Mode
```yaml
# In config.yaml
midi:
  debug_mode: true
```

## Building from Source

```bash
go build -o ha-midi .
```

## License

GNU Affero General Public License v3.0 (AGPL-3.0) - See [LICENSE](LICENSE) file for full text.

This means:
- ✅ Commercial use allowed
- ✅ Modification allowed
- ✅ Distribution allowed
- ✅ Patent use allowed
- ⚠️ Must disclose source code (including for network use)
- ⚠️ Must include license and copyright notice
- ⚠️ Must state changes made
- ⚠️ Derivatives must use same license (AGPL-3.0)