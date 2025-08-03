package main

import (
	"fmt"
	"io/ioutil"
	"time"

	"gopkg.in/yaml.v2"
)

// DeviceConfig represents per-device MIDI configuration
type DeviceConfig struct {
	Name             string `yaml:"name"`                       // Device name or pattern to match
	LightOffMethod   string `yaml:"light_off_method,omitempty"` // "note_off", "velocity_zero", "velocity_one", "multi"
	DebugMode        *bool  `yaml:"debug_mode,omitempty"`       // Log all MIDI inputs for troubleshooting
	RelativeEncoders struct {
		Enabled        *bool         `yaml:"enabled,omitempty"`         // Enable relative encoder support
		StepSize       *int          `yaml:"step_size,omitempty"`       // How much to increment/decrement per step
		SendInterval   time.Duration `yaml:"send_interval,omitempty"`   // How often to send accumulated changes
		IncrementValue *int          `yaml:"increment_value,omitempty"` // MIDI value that means increment
		DecrementValue *int          `yaml:"decrement_value,omitempty"` // MIDI value that means decrement
	} `yaml:"relative_encoders,omitempty"`
}

// Config represents the application configuration
type Config struct {
	MQTT struct {
		Broker          string `yaml:"broker"`
		Port            int    `yaml:"port"`
		Username        string `yaml:"username"`
		Password        string `yaml:"password"`
		ClientID        string `yaml:"client_id"`
		DiscoveryPrefix string `yaml:"discovery_prefix"`
	} `yaml:"mqtt"`

	MIDI struct {
		AutoConnect       bool          `yaml:"auto_connect"`
		ReconnectInterval time.Duration `yaml:"reconnect_interval"`
		PortScanInterval  time.Duration `yaml:"port_scan_interval"`   // How often to refresh MIDI port list from ALSA (default: 30s)
		LightOffMethod    string        `yaml:"light_off_method"`     // "note_off", "velocity_zero", "velocity_one", "multi"
		DebugMode         bool          `yaml:"debug_mode"`           // Log all MIDI inputs for troubleshooting
		PitchBendMaxValue int16         `yaml:"pitch_bend_max_value"` // Hardware-specific maximum pitch bend value (default: 8191)
		RelativeEncoders  struct {
			Enabled        bool          `yaml:"enabled"`         // Enable relative encoder support
			StepSize       int           `yaml:"step_size"`       // How much to increment/decrement per step (default: 1)
			SendInterval   time.Duration `yaml:"send_interval"`   // How often to send accumulated changes (default: 100ms)
			IncrementValue int           `yaml:"increment_value"` // MIDI value that means increment (default: 1)
			DecrementValue int           `yaml:"decrement_value"` // MIDI value that means decrement (default: 65)
		} `yaml:"relative_encoders"`
		Devices []DeviceConfig `yaml:"devices,omitempty"` // Per-device configurations
	} `yaml:"midi"`

	Bridge struct {
		ID   string `yaml:"id"`   // Unique bridge identifier
		Name string `yaml:"name"` // Human-readable bridge name
	} `yaml:"bridge"`

	HomeAssistant struct {
		DeviceName         string `yaml:"device_name"`
		DeviceManufacturer string `yaml:"device_manufacturer"`
		DeviceModel        string `yaml:"device_model"`
	} `yaml:"homeassistant"`

	Logging struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"`
	} `yaml:"logging"`
}

// GetDeviceConfig returns the device-specific configuration for a given device name
// Falls back to global configuration if no device-specific config is found
func (c *Config) GetDeviceConfig(deviceName string) *DeviceConfig {
	// Try to find device-specific config
	for _, deviceConfig := range c.MIDI.Devices {
		if deviceConfig.Name == deviceName {
			return &deviceConfig
		}
	}
	// Return nil if no device-specific config found - caller should use global config
	return nil
}

// GetDeviceLightOffMethod returns the light off method for a specific device
func (c *Config) GetDeviceLightOffMethod(deviceName string) string {
	if deviceConfig := c.GetDeviceConfig(deviceName); deviceConfig != nil && deviceConfig.LightOffMethod != "" {
		return deviceConfig.LightOffMethod
	}
	return c.MIDI.LightOffMethod
}

// GetDeviceDebugMode returns the debug mode setting for a specific device
func (c *Config) GetDeviceDebugMode(deviceName string) bool {
	if deviceConfig := c.GetDeviceConfig(deviceName); deviceConfig != nil && deviceConfig.DebugMode != nil {
		return *deviceConfig.DebugMode
	}
	return c.MIDI.DebugMode
}

// GetDeviceRelativeEncodersEnabled returns the relative encoders enabled setting for a specific device
func (c *Config) GetDeviceRelativeEncodersEnabled(deviceName string) bool {
	if deviceConfig := c.GetDeviceConfig(deviceName); deviceConfig != nil && deviceConfig.RelativeEncoders.Enabled != nil {
		return *deviceConfig.RelativeEncoders.Enabled
	}
	return c.MIDI.RelativeEncoders.Enabled
}

// GetDeviceRelativeEncodersStepSize returns the relative encoders step size for a specific device
func (c *Config) GetDeviceRelativeEncodersStepSize(deviceName string) int {
	if deviceConfig := c.GetDeviceConfig(deviceName); deviceConfig != nil && deviceConfig.RelativeEncoders.StepSize != nil {
		return *deviceConfig.RelativeEncoders.StepSize
	}
	return c.MIDI.RelativeEncoders.StepSize
}

// GetDeviceRelativeEncodersSendInterval returns the relative encoders send interval for a specific device
func (c *Config) GetDeviceRelativeEncodersSendInterval(deviceName string) time.Duration {
	if deviceConfig := c.GetDeviceConfig(deviceName); deviceConfig != nil && deviceConfig.RelativeEncoders.SendInterval != 0 {
		return deviceConfig.RelativeEncoders.SendInterval
	}
	return c.MIDI.RelativeEncoders.SendInterval
}

// GetDeviceRelativeEncodersIncrementValue returns the relative encoders increment value for a specific device
func (c *Config) GetDeviceRelativeEncodersIncrementValue(deviceName string) int {
	if deviceConfig := c.GetDeviceConfig(deviceName); deviceConfig != nil && deviceConfig.RelativeEncoders.IncrementValue != nil {
		return *deviceConfig.RelativeEncoders.IncrementValue
	}
	return c.MIDI.RelativeEncoders.IncrementValue
}

// GetDeviceRelativeEncodersDecrementValue returns the relative encoders decrement value for a specific device
func (c *Config) GetDeviceRelativeEncodersDecrementValue(deviceName string) int {
	if deviceConfig := c.GetDeviceConfig(deviceName); deviceConfig != nil && deviceConfig.RelativeEncoders.DecrementValue != nil {
		return *deviceConfig.RelativeEncoders.DecrementValue
	}
	return c.MIDI.RelativeEncoders.DecrementValue
}

func LoadConfig(filename string) (*Config, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	var config Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file: %v", err)
	}

	// Set defaults if not specified
	if config.MQTT.Port == 0 {
		config.MQTT.Port = 1883
	}
	if config.MQTT.ClientID == "" {
		config.MQTT.ClientID = "ha-midi-bridge"
	}
	if config.MQTT.DiscoveryPrefix == "" {
		config.MQTT.DiscoveryPrefix = "homeassistant"
	}
	if config.MIDI.LightOffMethod == "" {
		config.MIDI.LightOffMethod = "multi" // Default to multi-method approach
	}
	if config.MIDI.PitchBendMaxValue == 0 {
		config.MIDI.PitchBendMaxValue = 8191 // MIDI standard maximum
	}
	if config.MIDI.PortScanInterval == 0 {
		config.MIDI.PortScanInterval = 30 * time.Second // Default to 30 seconds to prevent ALSA resource exhaustion
	}
	// Set relative encoder defaults
	if config.MIDI.RelativeEncoders.StepSize == 0 {
		config.MIDI.RelativeEncoders.StepSize = 1
	}
	if config.MIDI.RelativeEncoders.SendInterval == 0 {
		config.MIDI.RelativeEncoders.SendInterval = 100 * time.Millisecond
	}
	if config.MIDI.RelativeEncoders.IncrementValue == 0 {
		config.MIDI.RelativeEncoders.IncrementValue = 1
	}
	if config.MIDI.RelativeEncoders.DecrementValue == 0 {
		config.MIDI.RelativeEncoders.DecrementValue = 65
	}

	// Set bridge defaults
	if config.Bridge.ID == "" {
		config.Bridge.ID = "ha-midi-bridge"
	}
	if config.Bridge.Name == "" {
		config.Bridge.Name = "HA-MIDI Bridge"
	}

	return &config, nil
}
