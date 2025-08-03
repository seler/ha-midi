package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// HADevice represents a Home Assistant device
type HADevice struct {
	Identifiers  []string `json:"identifiers"`
	Name         string   `json:"name"`
	Manufacturer string   `json:"manufacturer"`
	Model        string   `json:"model"`
	SwVersion    string   `json:"sw_version,omitempty"`
}

// HAEntity represents a Home Assistant entity
type HAEntity struct {
	Device            HADevice `json:"device"`
	Name              string   `json:"name"`
	UniqueID          string   `json:"unique_id"`
	StateTopic        string   `json:"state_topic"`
	CommandTopic      string   `json:"command_topic,omitempty"`
	ValueTemplate     string   `json:"value_template,omitempty"`
	PayloadOn         string   `json:"payload_on,omitempty"`
	PayloadOff        string   `json:"payload_off,omitempty"`
	StateOn           string   `json:"state_on,omitempty"`
	StateOff          string   `json:"state_off,omitempty"`
	Icon              string   `json:"icon,omitempty"`
	UnitOfMeasurement string   `json:"unit_of_measurement,omitempty"`
	Min               int      `json:"min,omitempty"`
	Max               int      `json:"max,omitempty"`
	DeviceClass       string   `json:"device_class,omitempty"`
	EntityCategory    string   `json:"entity_category,omitempty"`
	// Light-specific properties
	BrightnessCommandTopic string `json:"brightness_command_topic,omitempty"`
	BrightnessStateTopic   string `json:"brightness_state_topic,omitempty"`
	BrightnessScale        int    `json:"brightness_scale,omitempty"`
	// Binary sensor specific properties
	OffDelay int `json:"off_delay,omitempty"`
	// Availability support
	AvailabilityTopic   string `json:"availability_topic,omitempty"`
	PayloadAvailable    string `json:"payload_available,omitempty"`
	PayloadNotAvailable string `json:"payload_not_available,omitempty"`
}

// MIDIDevice represents a MIDI device and its entities
type MIDIDevice struct {
	ID                string
	Name              string
	DeviceName        string // Actual device name for configuration lookup
	HADevice          HADevice
	Entities          map[string]*HAEntity
	EntityStates      map[string]interface{}   // Track current state of each entity to prevent duplicate messages
	AvailabilityTopic string                   // Common availability topic for all entities of this device
	RelativeEncoders  map[string]int           // Track relative encoder accumulated change amounts
	CurrentChords     map[uint8]map[uint8]bool // Track currently pressed notes per channel [channel][note] = pressed
}

// CreateMIDIDevice creates a new MIDI device for Home Assistant
func CreateMIDIDevice(deviceID, deviceName string, config *Config) *MIDIDevice {
	availabilityTopic := fmt.Sprintf("ha-midi/%s/%s/availability", config.Bridge.ID, deviceID)

	// Create unique device identifier that includes bridge ID
	uniqueDeviceID := fmt.Sprintf("%s_%s", config.Bridge.ID, deviceID)

	// Create a display name that includes the bridge name and cleans up MIDI port info
	// Clean up common MIDI device name patterns
	cleanDeviceName := deviceName
	
	// Remove ALSA port numbers at the end (e.g., " 24:1", " 24")
	if matched, _ := regexp.MatchString(`\s+\d+(:\d+)?$`, cleanDeviceName); matched {
		cleanDeviceName = regexp.MustCompile(`\s+\d+(:\d+)?$`).ReplaceAllString(cleanDeviceName, "")
	}
	
	// Handle "MANUFACTURER:MANUFACTURER DEVICE" pattern (common in ALSA)
	if colonIndex := strings.Index(cleanDeviceName, ":"); colonIndex != -1 {
		beforeColon := strings.TrimSpace(cleanDeviceName[:colonIndex])
		afterColon := strings.TrimSpace(cleanDeviceName[colonIndex+1:])
		
		// If the part before colon is a prefix of what comes after, remove the redundant part
		if strings.HasPrefix(strings.ToLower(afterColon), strings.ToLower(beforeColon)) {
			cleanDeviceName = afterColon
		} else {
			// Otherwise just take what's after the colon as it's likely the device name
			cleanDeviceName = afterColon
		}
	}

	// Create display name with bridge prefix (no colon separator since bridge name should be descriptive)
	displayName := fmt.Sprintf("%s %s", config.Bridge.Name, cleanDeviceName)

	device := &MIDIDevice{
		ID:         deviceID,
		Name:       deviceName,
		DeviceName: deviceName, // Store actual device name for config lookup
		HADevice: HADevice{
			Identifiers:  []string{uniqueDeviceID},
			Name:         displayName,
			Manufacturer: config.HomeAssistant.DeviceManufacturer,
			Model:        config.HomeAssistant.DeviceModel,
			SwVersion:    "1.0.0",
		},
		Entities:          make(map[string]*HAEntity),
		EntityStates:      make(map[string]interface{}),
		AvailabilityTopic: availabilityTopic,
		RelativeEncoders:  make(map[string]int),
		CurrentChords:     make(map[uint8]map[uint8]bool),
	}
	return device
}

// AddBinarySensorEntity adds a binary sensor entity for MIDI button state (read-only)
func (md *MIDIDevice) AddBinarySensorEntity(entityID, name string, config *Config) {
	uniqueID := fmt.Sprintf("%s_%s_%s", config.Bridge.ID, md.ID, entityID)
	stateTopic := fmt.Sprintf("ha-midi/%s/%s/%s/state", config.Bridge.ID, md.ID, entityID)

	entity := &HAEntity{
		Device:              md.HADevice,
		Name:                name,
		UniqueID:            uniqueID,
		StateTopic:          stateTopic,
		PayloadOn:           "ON",
		PayloadOff:          "OFF",
		DeviceClass:         "sound", // Binary sensor device class for sound/music
		Icon:                "mdi:piano",
		AvailabilityTopic:   md.AvailabilityTopic,
		PayloadAvailable:    "online",
		PayloadNotAvailable: "offline",
	}

	md.Entities[entityID] = entity
}

// AddLightEntity adds a light entity for MIDI button LED control (write-only)
func (md *MIDIDevice) AddLightEntity(entityID, name string, config *Config) {
	uniqueID := fmt.Sprintf("%s_%s_%s", config.Bridge.ID, md.ID, entityID)
	stateTopic := fmt.Sprintf("ha-midi/%s/%s/%s/state", config.Bridge.ID, md.ID, entityID)
	commandTopic := fmt.Sprintf("ha-midi/%s/%s/%s/set", config.Bridge.ID, md.ID, entityID)

	entity := &HAEntity{
		Device:              md.HADevice,
		Name:                name,
		UniqueID:            uniqueID,
		StateTopic:          stateTopic,
		CommandTopic:        commandTopic,
		PayloadOn:           "ON",
		PayloadOff:          "OFF",
		Icon:                "mdi:led-on",
		AvailabilityTopic:   md.AvailabilityTopic,
		PayloadAvailable:    "online",
		PayloadNotAvailable: "offline",
	}

	md.Entities[entityID] = entity
}

// AddSwitchEntity adds a switch entity to the device
func (md *MIDIDevice) AddSwitchEntity(entityID, name string, config *Config) {
	uniqueID := fmt.Sprintf("%s_%s_%s", config.Bridge.ID, md.ID, entityID)
	stateTopic := fmt.Sprintf("ha-midi/%s/%s/%s/state", config.Bridge.ID, md.ID, entityID)
	commandTopic := fmt.Sprintf("ha-midi/%s/%s/%s/set", config.Bridge.ID, md.ID, entityID)

	entity := &HAEntity{
		Device:              md.HADevice,
		Name:                name,
		UniqueID:            uniqueID,
		StateTopic:          stateTopic,
		CommandTopic:        commandTopic,
		PayloadOn:           "ON",
		PayloadOff:          "OFF",
		StateOn:             "ON",
		StateOff:            "OFF",
		Icon:                "mdi:piano",
		AvailabilityTopic:   md.AvailabilityTopic,
		PayloadAvailable:    "online",
		PayloadNotAvailable: "offline",
	}

	md.Entities[entityID] = entity
}

// AddNumberEntity adds a number entity (for volume/fader controls)
func (md *MIDIDevice) AddNumberEntity(entityID, name string, min, max int, config *Config) {
	uniqueID := fmt.Sprintf("%s_%s_%s", config.Bridge.ID, md.ID, entityID)
	stateTopic := fmt.Sprintf("ha-midi/%s/%s/%s/state", config.Bridge.ID, md.ID, entityID)
	commandTopic := fmt.Sprintf("ha-midi/%s/%s/%s/set", config.Bridge.ID, md.ID, entityID)

	entity := &HAEntity{
		Device:              md.HADevice,
		Name:                name,
		UniqueID:            uniqueID,
		StateTopic:          stateTopic,
		CommandTopic:        commandTopic,
		Icon:                "mdi:tune-vertical",
		Min:                 min,
		Max:                 max,
		AvailabilityTopic:   md.AvailabilityTopic,
		PayloadAvailable:    "online",
		PayloadNotAvailable: "offline",
	}

	md.Entities[entityID] = entity
}

// AddPercentageEntity adds a percentage number entity (for pitch bend controls)
func (md *MIDIDevice) AddPercentageEntity(entityID, name string, config *Config) {
	uniqueID := fmt.Sprintf("%s_%s_%s", config.Bridge.ID, md.ID, entityID)
	stateTopic := fmt.Sprintf("ha-midi/%s/%s/%s/state", config.Bridge.ID, md.ID, entityID)
	commandTopic := fmt.Sprintf("ha-midi/%s/%s/%s/set", config.Bridge.ID, md.ID, entityID)

	entity := &HAEntity{
		Device:              md.HADevice,
		Name:                name,
		UniqueID:            uniqueID,
		StateTopic:          stateTopic,
		CommandTopic:        commandTopic,
		Icon:                "mdi:tune",
		Min:                 0,
		Max:                 100,
		UnitOfMeasurement:   "%",
		AvailabilityTopic:   md.AvailabilityTopic,
		PayloadAvailable:    "online",
		PayloadNotAvailable: "offline",
	}

	md.Entities[entityID] = entity
}

// AddReadOnlyNumberEntity adds a read-only number sensor entity (for control change values)
func (md *MIDIDevice) AddReadOnlyNumberEntity(entityID, name string, min, max int, config *Config) {
	uniqueID := fmt.Sprintf("%s_%s_%s", config.Bridge.ID, md.ID, entityID)
	stateTopic := fmt.Sprintf("ha-midi/%s/%s/%s/state", config.Bridge.ID, md.ID, entityID)

	entity := &HAEntity{
		Device:     md.HADevice,
		Name:       name,
		UniqueID:   uniqueID,
		StateTopic: stateTopic,
		// No CommandTopic - makes it read-only
		Icon: "mdi:tune-vertical",
		// Note: Min/Max not included for sensor entities - only for number entities
		AvailabilityTopic:   md.AvailabilityTopic,
		PayloadAvailable:    "online",
		PayloadNotAvailable: "offline",
	}

	md.Entities[entityID] = entity
}

// AddReadOnlyPercentageEntity adds a read-only percentage sensor entity (for pitch bend values)
func (md *MIDIDevice) AddReadOnlyPercentageEntity(entityID, name string, config *Config) {
	uniqueID := fmt.Sprintf("%s_%s_%s", config.Bridge.ID, md.ID, entityID)
	stateTopic := fmt.Sprintf("ha-midi/%s/%s/%s/state", config.Bridge.ID, md.ID, entityID)

	entity := &HAEntity{
		Device:     md.HADevice,
		Name:       name,
		UniqueID:   uniqueID,
		StateTopic: stateTopic,
		// No CommandTopic - makes it read-only
		Icon:              "mdi:tune",
		UnitOfMeasurement: "%",
		// Note: Min/Max not included for sensor entities - only for number entities
		AvailabilityTopic:   md.AvailabilityTopic,
		PayloadAvailable:    "online",
		PayloadNotAvailable: "offline",
	}

	md.Entities[entityID] = entity
}

// AddRelativeEncoderEntity adds a relative encoder entity that tracks accumulated values
func (md *MIDIDevice) AddRelativeEncoderEntity(entityID, name string, config *Config) {
	uniqueID := fmt.Sprintf("%s_%s_%s", config.Bridge.ID, md.ID, entityID)
	stateTopic := fmt.Sprintf("ha-midi/%s/%s/%s/state", config.Bridge.ID, md.ID, entityID)

	entity := &HAEntity{
		Device:     md.HADevice,
		Name:       name,
		UniqueID:   uniqueID,
		StateTopic: stateTopic,
		// No CommandTopic - makes it read-only
		Icon: "mdi:rotate-right",
		// Note: Min/Max not included for sensor entities - only for number entities
		AvailabilityTopic:   md.AvailabilityTopic,
		PayloadAvailable:    "online",
		PayloadNotAvailable: "offline",
	}

	md.Entities[entityID] = entity

	// Initialize relative encoder change accumulator if not exists
	if _, exists := md.RelativeEncoders[entityID]; !exists {
		md.RelativeEncoders[entityID] = 0 // Start with zero accumulated change
	}
}

// AddChordEntity adds a chord entity that shows currently pressed notes
func (md *MIDIDevice) AddChordEntity(entityID, name string, config *Config) {
	uniqueID := fmt.Sprintf("%s_%s_%s", config.Bridge.ID, md.ID, entityID)
	stateTopic := fmt.Sprintf("ha-midi/%s/%s/%s/state", config.Bridge.ID, md.ID, entityID)

	entity := &HAEntity{
		Device:     md.HADevice,
		Name:       name,
		UniqueID:   uniqueID,
		StateTopic: stateTopic,
		// No CommandTopic - makes it read-only
		Icon:                "mdi:piano",
		AvailabilityTopic:   md.AvailabilityTopic,
		PayloadAvailable:    "online",
		PayloadNotAvailable: "offline",
	}

	md.Entities[entityID] = entity

	// Initialize chord tracking for this channel if not exists
	channel := uint8(0) // Extract channel from entityID if needed later
	if _, exists := md.CurrentChords[channel]; !exists {
		md.CurrentChords[channel] = make(map[uint8]bool)
	}
}

// AddNoteToChord adds a note to the current chord
func (md *MIDIDevice) AddNoteToChord(channel, note uint8) {
	if _, exists := md.CurrentChords[channel]; !exists {
		md.CurrentChords[channel] = make(map[uint8]bool)
	}
	md.CurrentChords[channel][note] = true
}

// RemoveNoteFromChord removes a note from the current chord
func (md *MIDIDevice) RemoveNoteFromChord(channel, note uint8) {
	if chordMap, exists := md.CurrentChords[channel]; exists {
		delete(chordMap, note)
	}
}

// GetCurrentChord returns the current chord as a formatted string
func (md *MIDIDevice) GetCurrentChord(channel uint8) string {
	if chordMap, exists := md.CurrentChords[channel]; exists && len(chordMap) > 0 {
		var notes []uint8
		for note := range chordMap {
			notes = append(notes, note)
		}

		// Sort notes numerically for consistent output
		for i := 0; i < len(notes)-1; i++ {
			for j := i + 1; j < len(notes); j++ {
				if notes[i] > notes[j] {
					notes[i], notes[j] = notes[j], notes[i]
				}
			}
		}

		// Join notes with commas, formatted as absolute numbers
		result := ""
		for i, note := range notes {
			if i > 0 {
				result += ","
			}
			result += fmt.Sprintf("%d", note)
		}
		return result
	}
	return ""
}

// noteToName converts a MIDI note number to note name (e.g., 60 -> "C4")
func (md *MIDIDevice) noteToName(note uint8) string {
	noteNames := []string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}
	octave := int(note)/12 - 1
	noteName := noteNames[note%12]
	return fmt.Sprintf("%s%d", noteName, octave)
}

// UpdateExistingEntitiesForReadOnly updates existing entities to proper editable/read-only state
func (md *MIDIDevice) UpdateExistingEntitiesForReadOnly(config *Config) []string {
	var updatedEntities []string

	for entityID, entity := range md.Entities {
		// Check if this is a CC entity that needs updating
		if strings.HasPrefix(entityID, "cc_") && entity.CommandTopic != "" {
			// Parse the entity ID to get controller and channel
			parts := strings.Split(entityID, "_")
			if len(parts) >= 3 {
				controller, err1 := strconv.Atoi(parts[1])
				channel, err2 := strconv.Atoi(parts[2])
				if err1 == nil && err2 == nil {
					entityName := fmt.Sprintf("Control Change %d Channel %d", controller, channel)
					md.AddNumberEntity(entityID, entityName, 0, 127, config)
					updatedEntities = append(updatedEntities, entityID)
				}
			}
		}

		// Check if this is a PB entity that needs updating
		if strings.HasPrefix(entityID, "pb_") && entity.CommandTopic != "" {
			// Parse the entity ID to get channel
			parts := strings.Split(entityID, "_")
			if len(parts) >= 2 {
				channel, err := strconv.Atoi(parts[1])
				if err == nil {
					entityName := fmt.Sprintf("Pitch Bend Channel %d", channel)
					md.AddPercentageEntity(entityID, entityName, config)
					updatedEntities = append(updatedEntities, entityID)
				}
			}
		}
	}

	return updatedEntities
}

// GetDiscoveryTopic returns the MQTT discovery topic for an entity
func (md *MIDIDevice) GetDiscoveryTopic(entityID, entityType, discoveryPrefix, bridgeID string) string {
	return fmt.Sprintf("%s/%s/%s_%s_%s/config", discoveryPrefix, entityType, bridgeID, md.ID, entityID)
}

// GetDiscoveryPayload returns the JSON payload for MQTT discovery
func (md *MIDIDevice) GetDiscoveryPayload(entityID string) ([]byte, error) {
	entity, exists := md.Entities[entityID]
	if !exists {
		return nil, fmt.Errorf("entity %s not found", entityID)
	}

	payload, err := json.Marshal(entity)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal entity %s: %v", entityID, err)
	}

	// Debug log the payload content (only for sensors to avoid spam)
	if strings.Contains(entityID, "cc_") || strings.Contains(entityID, "pb_") {
		fmt.Printf("DEBUG: Discovery payload for %s: %s\n", entityID, string(payload))
	}

	return payload, nil
}

// GetEntityType returns the Home Assistant entity type based on the entity
func (md *MIDIDevice) GetEntityType(entityID string) string {
	entity, exists := md.Entities[entityID]
	if !exists {
		return ""
	}

	// Check for binary sensor (no command topic, has device class, has on/off payloads)
	if entity.CommandTopic == "" && entity.DeviceClass != "" && entity.PayloadOn != "" && entity.PayloadOff != "" {
		return "binary_sensor"
	}

	// Check for light entity (has command topic and led icon)
	if entity.CommandTopic != "" && entity.Icon == "mdi:led-on" {
		return "light"
	}

	// Check for switch entity (has command topic and on/off payloads)
	if entity.PayloadOn != "" && entity.PayloadOff != "" && entity.CommandTopic != "" {
		return "switch"
	}

	// Check for number entity (has min/max values AND has command topic)
	if (entity.Min != 0 || entity.Max != 0) && entity.CommandTopic != "" {
		return "number"
	}

	// Check for relative encoder entities (no command topic, has rotate-right icon)
	if entity.CommandTopic == "" && entity.Icon == "mdi:rotate-right" {
		return "sensor"
	}

	// Check for chord entities (no command topic, has piano icon, no unit of measurement)
	if entity.CommandTopic == "" && entity.Icon == "mdi:piano" && entity.UnitOfMeasurement == "" {
		return "sensor"
	}

	// Check for read-only sensor entities (no command topic, no on/off payloads, has state topic)
	if entity.CommandTopic == "" && entity.StateTopic != "" && entity.PayloadOn == "" && entity.PayloadOff == "" {
		return "sensor"
	}

	// Default fallback
	return "sensor"
}

// IsReadOnlyEntity checks if an entity is read-only (binary sensor)
func (md *MIDIDevice) IsReadOnlyEntity(entityID string) bool {
	entity, exists := md.Entities[entityID]
	if !exists {
		return false
	}
	return entity.CommandTopic == ""
}

// IsWriteOnlyEntity checks if an entity is primarily for output (light)
func (md *MIDIDevice) IsWriteOnlyEntity(entityID string) bool {
	entityType := md.GetEntityType(entityID)
	return entityType == "light"
}

// SanitizeEntityID converts a string to a valid entity ID
func SanitizeEntityID(input string) string {
	// Replace spaces and special characters with underscores
	sanitized := strings.ReplaceAll(input, " ", "_")
	sanitized = strings.ReplaceAll(sanitized, "-", "_")
	sanitized = strings.ToLower(sanitized)

	// Remove any character that's not alphanumeric or underscore
	var result strings.Builder
	for _, char := range sanitized {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' {
			result.WriteRune(char)
		}
	}

	return result.String()
}
