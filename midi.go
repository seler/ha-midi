package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/drivers"
	_ "gitlab.com/gomidi/midi/v2/drivers/rtmididrv" // autoregisters driver
)

// MIDIHandler handles MIDI device connections and events
type MIDIHandler struct {
	config         *Config
	logger         *logrus.Logger
	mqttClient     *MQTTClient
	devices        map[string]*MIDIDevice
	stopFuncs      map[string]func() // Keep track of stop functions for each device
	mutex          sync.RWMutex
	stopChan       chan struct{}
	wg             sync.WaitGroup
	encoderTimers  map[string]*time.Timer // Track timers for relative encoders
	lastPortScan   time.Time              // Track when we last got port list to avoid excessive ALSA calls
	cachedInPorts  []drivers.In           // Cache input ports to reduce ALSA client creation
	cachedOutPorts []drivers.Out          // Cache output ports to reduce ALSA client creation
	lastOutScan    time.Time              // Track when we last got output port list
	activeSenders  map[string]func()      // Track active MIDI senders for cleanup
}

// NewMIDIHandler creates a new MIDI handler
func NewMIDIHandler(config *Config, logger *logrus.Logger, mqttClient *MQTTClient) *MIDIHandler {
	return &MIDIHandler{
		config:        config,
		logger:        logger,
		mqttClient:    mqttClient,
		devices:       make(map[string]*MIDIDevice),
		stopFuncs:     make(map[string]func()),
		stopChan:      make(chan struct{}),
		encoderTimers: make(map[string]*time.Timer),
		activeSenders: make(map[string]func()),
	}
}

// Start begins MIDI device monitoring
func (mh *MIDIHandler) Start() error {
	mh.logger.Info("Starting MIDI handler")

	// Start device discovery
	mh.wg.Add(1)
	go mh.deviceDiscoveryLoop()

	// Start periodic sender cleanup
	mh.wg.Add(1)
	go mh.senderCleanupLoop()

	return nil
}

// Stop stops MIDI device monitoring
func (mh *MIDIHandler) Stop() {
	mh.logger.Info("Stopping MIDI handler")

	close(mh.stopChan)
	mh.wg.Wait()

	// Cancel all active encoder timers
	for timerKey, timer := range mh.encoderTimers {
		timer.Stop()
		delete(mh.encoderTimers, timerKey)
	}

	// Clean up all active MIDI senders
	for senderKey, closeFunc := range mh.activeSenders {
		mh.logger.WithField("sender", senderKey).Debug("Closing MIDI sender")
		closeFunc()
		delete(mh.activeSenders, senderKey)
	}

	// Stop all device listeners and set devices offline
	mh.mutex.Lock()
	defer mh.mutex.Unlock()

	for deviceID, stopFunc := range mh.stopFuncs {
		stopFunc()
		if device, exists := mh.devices[deviceID]; exists {
			if err := mh.mqttClient.SetDeviceOffline(device); err != nil {
				mh.logger.WithError(err).WithField("device_id", deviceID).Error("Failed to set device offline")
			}
		}
	}

	// Close the driver (this will clean up any remaining ALSA resources)
	midi.CloseDriver()
}

// deviceDiscoveryLoop continuously scans for MIDI devices
func (mh *MIDIHandler) deviceDiscoveryLoop() {
	defer mh.wg.Done()

	ticker := time.NewTicker(mh.config.MIDI.ReconnectInterval)
	defer ticker.Stop()

	// Initial scan
	mh.scanDevices()

	// List all discovered devices after initial scan
	mh.listDiscoveredDevices()

	// Update existing entities to be read-only for CC and PB after initial scan
	mh.updateExistingDeviceEntities()

	for {
		select {
		case <-mh.stopChan:
			return
		case <-ticker.C:
			mh.scanDevices()
		}
	}
}

// senderCleanupLoop periodically cleans up inactive MIDI senders
func (mh *MIDIHandler) senderCleanupLoop() {
	defer mh.wg.Done()

	// Clean up senders every 5 minutes
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-mh.stopChan:
			return
		case <-ticker.C:
			mh.cleanupInactiveSenders()
		}
	}
}

// cleanupInactiveSenders removes MIDI senders that haven't been used recently
func (mh *MIDIHandler) cleanupInactiveSenders() {
	mh.mutex.Lock()
	defer mh.mutex.Unlock()

	// For now, we'll keep senders active as they're lightweight
	// In the future, we could track last-used timestamps and clean up old ones
	// This is mainly here as a framework for future improvements
	mh.logger.WithField("active_senders", len(mh.activeSenders)).Debug("MIDI sender cleanup check completed")
}

// listDiscoveredDevices lists all currently discovered MIDI devices
func (mh *MIDIHandler) listDiscoveredDevices() {
	// Use cached ports to avoid creating unnecessary ALSA clients
	ins := mh.getCachedInPorts()
	outs := mh.getCachedOutPorts()

	if len(ins) == 0 {
		mh.logger.Info("No MIDI input devices discovered")
	} else {
		mh.logger.WithField("count", len(ins)).Info("MIDI input devices discovered:")
		for i, in := range ins {
			deviceName := in.String()
			deviceID := mh.generateDeviceID(deviceName, i)

			// Check if we have device-specific configuration
			hasDeviceConfig := mh.config.GetDeviceConfig(deviceName) != nil

			mh.logger.WithFields(logrus.Fields{
				"index":             i + 1,
				"device_name":       deviceName,
				"device_id":         deviceID,
				"has_device_config": hasDeviceConfig,
			}).Info("  - MIDI input device")
		}
	}

	if len(outs) == 0 {
		mh.logger.Warn("No MIDI output devices discovered - commands cannot be sent to devices")
	} else {
		mh.logger.WithField("count", len(outs)).Info("MIDI output devices discovered:")
		for i, out := range outs {
			deviceName := out.String()

			mh.logger.WithFields(logrus.Fields{
				"index":       i + 1,
				"device_name": deviceName,
			}).Info("  - MIDI output device")
		}
	}

	// Also list device-specific configurations from config that don't have connected devices
	configuredDevices := make(map[string]bool)
	for _, deviceConfig := range mh.config.MIDI.Devices {
		configuredDevices[deviceConfig.Name] = false
	}

	// Mark configured devices that are connected
	for _, in := range ins {
		deviceName := in.String()
		if _, exists := configuredDevices[deviceName]; exists {
			configuredDevices[deviceName] = true
		}
	}

	// List configured devices that are not connected
	var disconnectedConfigured []string
	for deviceName, isConnected := range configuredDevices {
		if !isConnected {
			disconnectedConfigured = append(disconnectedConfigured, deviceName)
		}
	}

	if len(disconnectedConfigured) > 0 {
		mh.logger.WithField("devices", disconnectedConfigured).Warn("Configured MIDI devices not currently connected")
	}
}

// getCachedInPorts returns cached input ports or refreshes them if needed
func (mh *MIDIHandler) getCachedInPorts() []drivers.In {
	now := time.Now()
	shouldRefreshPorts := now.Sub(mh.lastPortScan) > mh.config.MIDI.PortScanInterval || len(mh.cachedInPorts) == 0

	if shouldRefreshPorts {
		mh.cachedInPorts = midi.GetInPorts()
		mh.lastPortScan = now
		mh.logger.Debug("Refreshed MIDI input port list from ALSA")
	}

	return mh.cachedInPorts
}

// getCachedOutPorts returns cached output ports or refreshes them if needed
func (mh *MIDIHandler) getCachedOutPorts() []drivers.Out {
	now := time.Now()
	shouldRefreshPorts := now.Sub(mh.lastOutScan) > mh.config.MIDI.PortScanInterval || len(mh.cachedOutPorts) == 0

	if shouldRefreshPorts {
		mh.cachedOutPorts = midi.GetOutPorts()
		mh.lastOutScan = now
		mh.logger.Debug("Refreshed MIDI output port list from ALSA")
	}

	return mh.cachedOutPorts
}

// createManagedSender creates a MIDI sender with proper resource management
func (mh *MIDIHandler) createManagedSender(out drivers.Out, senderKey string) (func(midi.Message) error, error) {
	// Check if we already have an active sender for this key
	if closeFunc, exists := mh.activeSenders[senderKey]; exists {
		mh.logger.WithField("sender_key", senderKey).Debug("Cleaning up existing MIDI sender before creating new one")
		closeFunc()
		delete(mh.activeSenders, senderKey)
	}

	send, err := midi.SendTo(out)
	if err != nil {
		return nil, err
	}

	// Create a cleanup function for this sender
	closeFunc := func() {
		// The midi library doesn't expose a direct close method for senders,
		// but closing the driver will clean up all resources
		mh.logger.WithField("sender_key", senderKey).Debug("MIDI sender cleanup completed")
	}

	// Store the cleanup function
	mh.activeSenders[senderKey] = closeFunc

	// Wrap the send function to provide automatic cleanup on errors
	wrappedSend := func(msg midi.Message) error {
		err := send(msg)
		if err != nil {
			// On error, clean up the sender
			mh.logger.WithError(err).WithField("sender_key", senderKey).Debug("MIDI send failed, cleaning up sender")
			if closeFunc, exists := mh.activeSenders[senderKey]; exists {
				closeFunc()
				delete(mh.activeSenders, senderKey)
			}
		}
		return err
	}

	return wrappedSend, nil
}

// scanDevices scans for available MIDI devices
func (mh *MIDIHandler) scanDevices() {
	// Use cached input ports to avoid excessive ALSA client creation
	ins := mh.getCachedInPorts()

	mh.mutex.Lock()
	defer mh.mutex.Unlock()

	// Track current device IDs
	currentDevices := make(map[string]bool)

	for i, in := range ins {
		deviceID := mh.generateDeviceID(in.String(), i)
		currentDevices[deviceID] = true

		// Skip if already connected
		if _, exists := mh.stopFuncs[deviceID]; exists {
			continue
		}

		// Connect to new device
		if err := mh.connectDevice(in, deviceID); err != nil {
			mh.logger.WithError(err).WithField("device_id", deviceID).Error("Failed to connect to MIDI device")
			// If connection fails, force a port refresh on next scan
			mh.lastPortScan = time.Time{}
			mh.lastOutScan = time.Time{}
			continue
		}

		mh.logger.WithField("device_id", deviceID).Info("Connected to MIDI device")
	}

	// Remove disconnected devices
	for deviceID := range mh.stopFuncs {
		if !currentDevices[deviceID] {
			mh.disconnectDevice(deviceID)
			// Force a port refresh after device disconnection
			mh.lastPortScan = time.Time{}
			mh.lastOutScan = time.Time{}
		}
	}
}

// generateDeviceID creates a unique device ID from MIDI device info
func (mh *MIDIHandler) generateDeviceID(name string, index int) string {
	// Create a sanitized device ID
	deviceID := SanitizeEntityID(name)
	if deviceID == "" {
		deviceID = fmt.Sprintf("midi_device_%d", index)
	}

	return deviceID
}

// connectDevice connects to a MIDI device and sets up event handling
func (mh *MIDIHandler) connectDevice(in drivers.In, deviceID string) error {
	// Handle device ID collision by appending counter if needed
	originalDeviceID := deviceID
	counter := 1
	for {
		_, exists := mh.devices[deviceID]
		if !exists {
			break
		}
		deviceID = fmt.Sprintf("%s_%d", originalDeviceID, counter)
		counter++
	}

	// Create device in Home Assistant
	device := CreateMIDIDevice(deviceID, in.String(), mh.config)
	mh.devices[deviceID] = device

	// Update existing entities to be read-only if they exist
	updatedEntities := device.UpdateExistingEntitiesForReadOnly(mh.config)
	if len(updatedEntities) > 0 {
		mh.logger.WithFields(logrus.Fields{
			"device_id": deviceID,
			"entities":  updatedEntities,
		}).Info("Updated existing entities to read-only on device connection")
	}

	// Publish device discovery information
	if err := mh.mqttClient.PublishDiscovery(device); err != nil {
		mh.logger.WithError(err).WithField("device_id", deviceID).Error("Failed to publish device discovery")
		// Don't return error, continue with connection
	}

	// Subscribe to command topics for controllable entities
	if err := mh.mqttClient.Subscribe(device); err != nil {
		mh.logger.WithError(err).WithField("device_id", deviceID).Error("Failed to subscribe to command topics")
		// Don't return error, continue with connection
	}

	// Publish device as online
	if err := mh.mqttClient.SetDeviceOnline(device); err != nil {
		mh.logger.WithError(err).WithField("device_id", deviceID).Error("Failed to publish device online status")
		// Don't return error, continue with connection
	}

	// Set up MIDI listening
	stop, err := midi.ListenTo(in, func(msg midi.Message, timestampms int32) {
		mh.processMIDIMessage(deviceID, msg)
	})
	if err != nil {
		return fmt.Errorf("failed to listen to MIDI device: %v", err)
	}

	mh.stopFuncs[deviceID] = stop

	return nil
}

// updateExistingDeviceEntities updates existing devices to use read-only CC and PB entities
func (mh *MIDIHandler) updateExistingDeviceEntities() {
	mh.mutex.Lock()
	defer mh.mutex.Unlock()

	for deviceID, device := range mh.devices {
		updatedEntities := device.UpdateExistingEntitiesForReadOnly(mh.config)

		if len(updatedEntities) > 0 {
			mh.logger.WithFields(logrus.Fields{
				"device_id": deviceID,
				"entities":  updatedEntities,
			}).Info("Updated existing entities to read-only")

			// Republish discovery for updated entities
			for _, entityID := range updatedEntities {
				mh.publishEntityDiscovery(device, entityID)
			}
		}
	}
}

// disconnectDevice disconnects from a MIDI device
func (mh *MIDIHandler) disconnectDevice(deviceID string) {
	mh.logger.WithField("device_id", deviceID).Info("Disconnecting MIDI device")

	// Stop listening
	if stopFunc, exists := mh.stopFuncs[deviceID]; exists {
		stopFunc()
		delete(mh.stopFuncs, deviceID)
	}

	// Set device as offline instead of removing discovery
	if device, exists := mh.devices[deviceID]; exists {
		if err := mh.mqttClient.SetDeviceOffline(device); err != nil {
			mh.logger.WithError(err).WithField("device_id", deviceID).Error("Failed to publish device offline status")
		}
		delete(mh.devices, deviceID)
	}
}

// processMIDIMessage processes individual MIDI messages
func (mh *MIDIHandler) processMIDIMessage(deviceID string, msg midi.Message) {
	mh.mutex.RLock()
	device, exists := mh.devices[deviceID]
	mh.mutex.RUnlock()

	if !exists {
		return
	}

	// Debug logging for all MIDI messages when debug mode is enabled
	if mh.config.MIDI.DebugMode {
		mh.logger.WithFields(logrus.Fields{
			"device_id":    deviceID,
			"device_name":  device.Name,
			"raw_message":  fmt.Sprintf("%v", msg),
			"message_type": msg.String(),
		}).Info("DEBUG: Received MIDI message")
	}

	// Parse different MIDI message types
	var channel, key, velocity, controller, value, program uint8
	var pitchBend int16
	var rawPitchBend uint16

	switch {
	case msg.GetNoteStart(&channel, &key, &velocity):
		if mh.config.MIDI.DebugMode {
			mh.logger.WithFields(logrus.Fields{
				"device_id": deviceID,
				"type":      "note_on",
				"channel":   channel,
				"key":       key,
				"velocity":  velocity,
			}).Info("DEBUG: Note On message")
		}
		mh.handleNoteOn(device, channel, key, velocity)
	case msg.GetNoteEnd(&channel, &key):
		if mh.config.MIDI.DebugMode {
			mh.logger.WithFields(logrus.Fields{
				"device_id": deviceID,
				"type":      "note_off",
				"channel":   channel,
				"key":       key,
			}).Info("DEBUG: Note Off message")
		}
		mh.handleNoteOff(device, channel, key)
	case msg.GetControlChange(&channel, &controller, &value):
		if mh.config.MIDI.DebugMode {
			mh.logger.WithFields(logrus.Fields{
				"device_id":  deviceID,
				"type":       "control_change",
				"channel":    channel,
				"controller": controller,
				"value":      value,
			}).Info("DEBUG: Control Change message")
		}
		mh.handleControlChange(device, channel, controller, value)
	case msg.GetProgramChange(&channel, &program):
		if mh.config.MIDI.DebugMode {
			mh.logger.WithFields(logrus.Fields{
				"device_id": deviceID,
				"type":      "program_change",
				"channel":   channel,
				"program":   program,
			}).Info("DEBUG: Program Change message")
		}
		mh.handleProgramChange(device, channel, program)
	case msg.GetPitchBend(&channel, &pitchBend, &rawPitchBend):
		if mh.config.MIDI.DebugMode {
			mh.logger.WithFields(logrus.Fields{
				"device_id":      deviceID,
				"type":           "pitch_bend",
				"channel":        channel,
				"pitch_bend":     pitchBend,
				"raw_pitch_bend": rawPitchBend,
			}).Info("DEBUG: Pitch Bend message")
		}
		mh.handlePitchBend(device, channel, pitchBend)
	default:
		if mh.config.MIDI.DebugMode {
			mh.logger.WithFields(logrus.Fields{
				"device_id":    deviceID,
				"type":         "unknown",
				"raw_message":  fmt.Sprintf("%v", msg),
				"message_type": msg.String(),
			}).Info("DEBUG: Unknown MIDI message type")
		}
	}
}

// handleNoteOn processes note on events
func (mh *MIDIHandler) handleNoteOn(device *MIDIDevice, channel, key, velocity uint8) {
	// Create entity IDs for both binary sensor and light using numeric format
	binarySensorID := fmt.Sprintf("button_state_%03d_%02d", key, channel)
	lightID := fmt.Sprintf("button_light_%03d_%02d", key, channel)

	baseName := fmt.Sprintf("Note %03d Channel %02d", key, channel)
	binarySensorName := fmt.Sprintf("%s State", baseName)
	lightName := fmt.Sprintf("%s Light", baseName)

	// Create binary sensor entity if it doesn't exist
	if _, exists := device.Entities[binarySensorID]; !exists {
		device.AddBinarySensorEntity(binarySensorID, binarySensorName, mh.config)
		mh.publishEntityDiscovery(device, binarySensorID)
	}

	// Create light entity if it doesn't exist
	if _, exists := device.Entities[lightID]; !exists {
		device.AddLightEntity(lightID, lightName, mh.config)
		mh.publishEntityDiscovery(device, lightID)
	}

	// Update binary sensor state (velocity > 0 means note on)
	state := velocity > 0
	if err := mh.mqttClient.PublishState(device, binarySensorID, state); err != nil {
		mh.logger.WithError(err).WithField("entity_id", binarySensorID).Error("Failed to publish button state")
	}

	// Update chord tracking
	if velocity > 0 {
		device.AddNoteToChord(channel, key)
	} else {
		// velocity 0 means note off
		device.RemoveNoteFromChord(channel, key)
	}
	mh.updateChordEntity(device, channel)
}

// handleNoteOff processes note off events
func (mh *MIDIHandler) handleNoteOff(device *MIDIDevice, channel, key uint8) {
	// Create entity IDs for both binary sensor and light using numeric format
	binarySensorID := fmt.Sprintf("button_state_%03d_%02d", key, channel)
	lightID := fmt.Sprintf("button_light_%03d_%02d", key, channel)

	baseName := fmt.Sprintf("Note %03d Channel %02d", key, channel)
	binarySensorName := fmt.Sprintf("%s State", baseName)
	lightName := fmt.Sprintf("%s Light", baseName)

	// Create binary sensor entity if it doesn't exist
	if _, exists := device.Entities[binarySensorID]; !exists {
		device.AddBinarySensorEntity(binarySensorID, binarySensorName, mh.config)
		mh.publishEntityDiscovery(device, binarySensorID)
	}

	// Create light entity if it doesn't exist
	if _, exists := device.Entities[lightID]; !exists {
		device.AddLightEntity(lightID, lightName, mh.config)
		mh.publishEntityDiscovery(device, lightID)
	}

	// Update binary sensor state (note off)
	if err := mh.mqttClient.PublishState(device, binarySensorID, false); err != nil {
		mh.logger.WithError(err).WithField("entity_id", binarySensorID).Error("Failed to publish button state")
	}

	// Update chord tracking
	device.RemoveNoteFromChord(channel, key)
	mh.updateChordEntity(device, channel)
}

// handleControlChange processes control change events (knobs, faders, etc.)
func (mh *MIDIHandler) handleControlChange(device *MIDIDevice, channel, controller, value uint8) {
	entityID := fmt.Sprintf("cc_%03d_%02d", controller, channel)
	entityName := fmt.Sprintf("Control Change %03d Channel %02d", controller, channel)

	// Check if relative encoders are enabled and if this looks like a relative encoder
	if mh.config.GetDeviceRelativeEncodersEnabled(device.DeviceName) && mh.isRelativeEncoder(device, int(value)) {
		// Handle as relative encoder
		mh.handleRelativeEncoder(device, entityID, entityName, int(value))
		return
	}

	// Handle as regular absolute CC
	// Create entity if it doesn't exist
	if _, exists := device.Entities[entityID]; !exists {
		mh.logger.WithFields(logrus.Fields{
			"entity_id":   entityID,
			"entity_name": entityName,
			"controller":  controller,
			"channel":     channel,
		}).Info("Creating new editable CC entity")

		device.AddNumberEntity(entityID, entityName, 0, 127, mh.config)
		mh.publishEntityDiscovery(device, entityID)
	}

	// Publish state
	if err := mh.mqttClient.PublishState(device, entityID, int(value)); err != nil {
		mh.logger.WithError(err).WithField("entity_id", entityID).Error("Failed to publish CC state")
	}
}

// isRelativeEncoder checks if a value represents a relative encoder movement
func (mh *MIDIHandler) isRelativeEncoder(device *MIDIDevice, value int) bool {
	return value == mh.config.GetDeviceRelativeEncodersIncrementValue(device.DeviceName) ||
		value == mh.config.GetDeviceRelativeEncodersDecrementValue(device.DeviceName)
}

// handleRelativeEncoder processes relative encoder events
func (mh *MIDIHandler) handleRelativeEncoder(device *MIDIDevice, entityID, entityName string, value int) {
	// Create entity if it doesn't exist
	if _, exists := device.Entities[entityID]; !exists {
		mh.logger.WithFields(logrus.Fields{
			"entity_id":   entityID,
			"entity_name": entityName,
			"device":      device.DeviceName,
		}).Info("Creating new relative encoder entity")

		device.AddRelativeEncoderEntity(entityID, entityName, mh.config)
		mh.publishEntityDiscovery(device, entityID)
	}

	// Get current accumulated change
	accumulatedChange := device.RelativeEncoders[entityID]

	// Update accumulated change based on increment/decrement
	incrementValue := mh.config.GetDeviceRelativeEncodersIncrementValue(device.DeviceName)
	decrementValue := mh.config.GetDeviceRelativeEncodersDecrementValue(device.DeviceName)
	stepSize := mh.config.GetDeviceRelativeEncodersStepSize(device.DeviceName)

	if value == incrementValue {
		accumulatedChange += stepSize
	} else if value == decrementValue {
		accumulatedChange -= stepSize
	}

	// Update stored accumulated change
	device.RelativeEncoders[entityID] = accumulatedChange

	// Create unique timer key
	timerKey := fmt.Sprintf("%s_%s", device.ID, entityID)

	// Cancel existing timer if it exists
	if timer, exists := mh.encoderTimers[timerKey]; exists {
		timer.Stop()
	}

	// Create new timer to send accumulated change after interval
	sendInterval := mh.config.GetDeviceRelativeEncodersSendInterval(device.DeviceName)
	mh.encoderTimers[timerKey] = time.AfterFunc(sendInterval, func() {
		mh.sendAccumulatedChange(device, entityID, timerKey)
	})

	debugMode := mh.config.GetDeviceDebugMode(device.DeviceName)
	if debugMode {
		mh.logger.WithFields(logrus.Fields{
			"entity_id":          entityID,
			"raw_value":          value,
			"accumulated_change": accumulatedChange,
			"device":             device.DeviceName,
			"direction": map[int]string{
				incrementValue: "increment",
				decrementValue: "decrement",
			}[value],
		}).Info("DEBUG: Relative encoder change accumulated")
	}
}

// sendAccumulatedChange sends the accumulated change amount for a relative encoder
func (mh *MIDIHandler) sendAccumulatedChange(device *MIDIDevice, entityID, timerKey string) {
	// Get the accumulated change
	accumulatedChange := device.RelativeEncoders[entityID]

	// Only send if there's a change
	if accumulatedChange != 0 {
		// Publish the accumulated change amount
		if err := mh.mqttClient.PublishState(device, entityID, accumulatedChange); err != nil {
			mh.logger.WithError(err).WithFields(logrus.Fields{
				"entity_id": entityID,
				"device":    device.DeviceName,
			}).Error("Failed to publish relative encoder change")
		}

		// Reset the accumulated change
		device.RelativeEncoders[entityID] = 0

		debugMode := mh.config.GetDeviceDebugMode(device.DeviceName)
		if debugMode {
			mh.logger.WithFields(logrus.Fields{
				"entity_id":          entityID,
				"accumulated_change": accumulatedChange,
				"device":             device.DeviceName,
			}).Info("DEBUG: Sent relative encoder change and reset accumulator")
		}
	}

	// Remove the timer from the map
	delete(mh.encoderTimers, timerKey)
}

// updateChordEntity updates the chord entity for a given channel
func (mh *MIDIHandler) updateChordEntity(device *MIDIDevice, channel uint8) {
	chordEntityID := fmt.Sprintf("chord_%d", channel)
	chordEntityName := fmt.Sprintf("Chord Channel %d", channel)

	// Create chord entity if it doesn't exist
	if _, exists := device.Entities[chordEntityID]; !exists {
		device.AddChordEntity(chordEntityID, chordEntityName, mh.config)
		mh.publishEntityDiscovery(device, chordEntityID)
	}

	// Get current chord and publish state
	currentChord := device.GetCurrentChord(channel)
	if err := mh.mqttClient.PublishState(device, chordEntityID, currentChord); err != nil {
		mh.logger.WithError(err).WithField("entity_id", chordEntityID).Error("Failed to publish chord state")
	}

	if mh.config.MIDI.DebugMode {
		mh.logger.WithFields(logrus.Fields{
			"entity_id": chordEntityID,
			"channel":   channel,
			"chord":     currentChord,
		}).Info("DEBUG: Updated chord entity")
	}
}

// handleProgramChange processes program change events
func (mh *MIDIHandler) handleProgramChange(device *MIDIDevice, channel, program uint8) {
	entityID := fmt.Sprintf("pc_%d", channel)
	entityName := fmt.Sprintf("Program Channel %d", channel)

	// Create entity if it doesn't exist
	if _, exists := device.Entities[entityID]; !exists {
		device.AddNumberEntity(entityID, entityName, 0, 127, mh.config)
		mh.publishEntityDiscovery(device, entityID)
	}

	// Publish state
	if err := mh.mqttClient.PublishState(device, entityID, int(program)); err != nil {
		mh.logger.WithError(err).WithField("entity_id", entityID).Error("Failed to publish program state")
	}
}

// handlePitchBend processes pitch bend events
func (mh *MIDIHandler) handlePitchBend(device *MIDIDevice, channel uint8, pitchBend int16) {
	entityID := fmt.Sprintf("pb_%d", channel)
	entityName := fmt.Sprintf("Pitch Bend Channel %d", channel)

	// Create entity if it doesn't exist (0-100% range with % symbol)
	if _, exists := device.Entities[entityID]; !exists {
		mh.logger.WithFields(logrus.Fields{
			"entity_id":   entityID,
			"entity_name": entityName,
			"channel":     channel,
		}).Info("Creating new editable PB entity")

		device.AddPercentageEntity(entityID, entityName, mh.config)
		mh.publishEntityDiscovery(device, entityID)
	}

	// Convert MIDI pitch bend range (-8192 to +configured_max) to percentage (0-100)
	// MIDI center (0) = 50%, min (-8192) = 0%, max (configured_max) = 100%
	maxValue := mh.config.MIDI.PitchBendMaxValue
	var percentage float64
	if pitchBend >= maxValue {
		// Ensure maximum pitch bend always maps to 100%
		percentage = 100.0
	} else {
		// Use the configured maximum for scaling
		totalRange := float64(maxValue + 8192) // Total range from -8192 to +maxValue
		percentage = float64(pitchBend+8192) / totalRange * 100.0
	}

	// Round to nearest integer
	percentageInt := int(percentage + 0.5)

	// Clamp to 0-100 range
	if percentageInt < 0 {
		percentageInt = 0
	}
	if percentageInt > 100 {
		percentageInt = 100
	}

	// Check if the value has changed to prevent duplicate messages
	if lastValue, exists := device.EntityStates[entityID]; exists {
		if lastPercentage, ok := lastValue.(int); ok && lastPercentage == percentageInt {
			// Value hasn't changed, skip publishing
			debugMode := mh.config.GetDeviceDebugMode(device.DeviceName)
			if debugMode {
				mh.logger.WithFields(logrus.Fields{
					"entity_id":      entityID,
					"raw_pitch_bend": pitchBend,
					"percentage":     percentageInt,
					"device":         device.DeviceName,
				}).Info("DEBUG: Pitch bend value unchanged, skipping duplicate message")
			}
			return
		}
	}

	// Update the stored state
	device.EntityStates[entityID] = percentageInt

	// Publish state as percentage
	if err := mh.mqttClient.PublishState(device, entityID, percentageInt); err != nil {
		mh.logger.WithError(err).WithField("entity_id", entityID).Error("Failed to publish pitch bend state")
	}

	debugMode := mh.config.GetDeviceDebugMode(device.DeviceName)
	if debugMode {
		mh.logger.WithFields(logrus.Fields{
			"entity_id":      entityID,
			"raw_pitch_bend": pitchBend,
			"percentage":     percentageInt,
			"device":         device.DeviceName,
		}).Info("DEBUG: Converted pitch bend to percentage")
	}
}

// publishEntityDiscovery publishes discovery for a single entity
func (mh *MIDIHandler) publishEntityDiscovery(device *MIDIDevice, entityID string) {
	entityType := device.GetEntityType(entityID)
	topic := device.GetDiscoveryTopic(entityID, entityType, mh.config.MQTT.DiscoveryPrefix, mh.config.Bridge.ID)

	payload, err := device.GetDiscoveryPayload(entityID)
	if err != nil {
		mh.logger.WithError(err).WithField("entity_id", entityID).Error("Failed to get discovery payload")
		return
	}

	mh.logger.WithFields(logrus.Fields{
		"entity_id":    entityID,
		"entity_type":  entityType,
		"topic":        topic,
		"payload_size": len(payload),
	}).Info("Publishing entity discovery")

	if err := mh.mqttClient.Publish(topic, payload, true); err != nil {
		mh.logger.WithError(err).WithField("entity_id", entityID).Error("Failed to publish discovery")
		return
	}
}

// createEntityOnDemand creates an entity based on its ID pattern if it doesn't exist
func (mh *MIDIHandler) createEntityOnDemand(device *MIDIDevice, entityID string) bool {
	// Parse entity ID to determine what type of entity to create
	parts := strings.Split(entityID, "_")
	if len(parts) < 2 {
		mh.logger.WithField("entity_id", entityID).Error("Invalid entity ID format for on-demand creation")
		return false
	}

	switch parts[0] {
	case "button":
		if parts[1] == "light" && len(parts) >= 4 {
			// button_light_{key}_{channel}
			key, err1 := strconv.Atoi(parts[2])
			channel, err2 := strconv.Atoi(parts[3])
			if err1 != nil || err2 != nil {
				mh.logger.WithFields(logrus.Fields{
					"entity_id":    entityID,
					"key_part":     parts[2],
					"channel_part": parts[3],
				}).Error("Failed to parse button light entity ID")
				return false
			}

			// Create both binary sensor and light entities for this button
			binarySensorID := fmt.Sprintf("button_state_%03d_%02d", key, channel)
			baseName := fmt.Sprintf("Note %03d Channel %02d", key, channel)
			binarySensorName := fmt.Sprintf("%s State", baseName)
			lightName := fmt.Sprintf("%s Light", baseName)

			// Create binary sensor if it doesn't exist
			if _, exists := device.Entities[binarySensorID]; !exists {
				device.AddBinarySensorEntity(binarySensorID, binarySensorName, mh.config)
				mh.publishEntityDiscovery(device, binarySensorID)
			}

			// Create light entity if it doesn't exist
			if _, exists := device.Entities[entityID]; !exists {
				device.AddLightEntity(entityID, lightName, mh.config)
				mh.publishEntityDiscovery(device, entityID)
			}

			mh.logger.WithFields(logrus.Fields{
				"entity_id": entityID,
				"key":       key,
				"channel":   channel,
			}).Info("Created button light entity on-demand")
			return true
		}

	case "cc":
		if len(parts) >= 3 {
			// cc_{controller}_{channel}
			controller, err1 := strconv.Atoi(parts[1])
			channel, err2 := strconv.Atoi(parts[2])
			if err1 != nil || err2 != nil {
				mh.logger.WithFields(logrus.Fields{
					"entity_id":       entityID,
					"controller_part": parts[1],
					"channel_part":    parts[2],
				}).Error("Failed to parse CC entity ID")
				return false
			}

			entityName := fmt.Sprintf("Control Change %d Channel %d", controller, channel)
			device.AddNumberEntity(entityID, entityName, 0, 127, mh.config)
			mh.publishEntityDiscovery(device, entityID)

			mh.logger.WithFields(logrus.Fields{
				"entity_id":  entityID,
				"controller": controller,
				"channel":    channel,
			}).Info("Created editable CC entity on-demand")
			return true
		}

	case "pc":
		if len(parts) >= 2 {
			// pc_{channel}
			channel, err := strconv.Atoi(parts[1])
			if err != nil {
				mh.logger.WithFields(logrus.Fields{
					"entity_id":    entityID,
					"channel_part": parts[1],
				}).Error("Failed to parse PC entity ID")
				return false
			}

			entityName := fmt.Sprintf("Program Channel %d", channel)
			device.AddNumberEntity(entityID, entityName, 0, 127, mh.config)
			mh.publishEntityDiscovery(device, entityID)

			mh.logger.WithFields(logrus.Fields{
				"entity_id": entityID,
				"channel":   channel,
			}).Info("Created PC entity on-demand")
			return true
		}

	case "pb":
		if len(parts) >= 2 {
			// pb_{channel}
			channel, err := strconv.Atoi(parts[1])
			if err != nil {
				mh.logger.WithFields(logrus.Fields{
					"entity_id":    entityID,
					"channel_part": parts[1],
				}).Error("Failed to parse PB entity ID")
				return false
			}

			entityName := fmt.Sprintf("Pitch Bend Channel %d", channel)
			device.AddReadOnlyPercentageEntity(entityID, entityName, mh.config)
			mh.publishEntityDiscovery(device, entityID)

			mh.logger.WithFields(logrus.Fields{
				"entity_id": entityID,
				"channel":   channel,
			}).Info("Created PB entity on-demand")
			return true
		}
	}

	mh.logger.WithFields(logrus.Fields{
		"entity_id": entityID,
		"parts":     parts,
	}).Warn("Unknown entity type for on-demand creation")
	return false
}

// HandleMQTTMessage processes MQTT messages from Home Assistant
func (mh *MIDIHandler) HandleMQTTMessage(topic string, payload []byte) {
	mh.logger.WithFields(logrus.Fields{
		"topic":   topic,
		"payload": string(payload),
	}).Info("Received MQTT command")

	// Parse topic to extract bridge, device and entity information
	// Format: ha-midi/{bridge_id}/{device_id}/{entity_id}/set
	parts := strings.Split(topic, "/")
	if len(parts) < 5 || parts[0] != "ha-midi" || parts[len(parts)-1] != "set" {
		mh.logger.WithField("topic", topic).Warn("Invalid MQTT topic format")
		return
	}

	bridgeID := parts[1]
	deviceID := parts[2]
	entityID := parts[3]
	command := string(payload)

	// Verify this is for our bridge
	if bridgeID != mh.config.Bridge.ID {
		mh.logger.WithFields(logrus.Fields{
			"topic":     topic,
			"bridge_id": bridgeID,
			"expected":  mh.config.Bridge.ID,
		}).Debug("Ignoring MQTT command for different bridge")
		return
	}

	mh.logger.WithFields(logrus.Fields{
		"device_id": deviceID,
		"entity_id": entityID,
		"command":   command,
	}).Info("Parsed MQTT command")

	mh.mutex.RLock()
	device, exists := mh.devices[deviceID]
	mh.mutex.RUnlock()

	if !exists {
		mh.logger.WithField("device_id", deviceID).Warn("Received command for unknown device")
		return
	}

	// Check if entity exists, create on demand if not
	entity, exists := device.Entities[entityID]
	if !exists {
		// Try to create entity on demand
		if !mh.createEntityOnDemand(device, entityID) {
			mh.logger.WithField("entity_id", entityID).Warn("Entity not found and could not be created on demand")
			return
		}
		// Get the entity again after creation
		entity, exists = device.Entities[entityID]
		if !exists {
			mh.logger.WithField("entity_id", entityID).Error("Entity creation failed")
			return
		}
	}

	// Only process commands for controllable entities (not binary sensors)
	if device.IsReadOnlyEntity(entityID) {
		mh.logger.WithField("entity_id", entityID).Debug("Ignoring command for read-only entity")
		return
	}

	// Send MIDI message based on entity type and command
	success := mh.sendMIDICommand(device, deviceID, entityID, entity, command)

	// Only update entity state if the MIDI command was sent successfully
	if success {
		commandValue := parseCommandValue(command)

		// Update the stored state to maintain consistency
		device.EntityStates[entityID] = commandValue

		if err := mh.mqttClient.PublishState(device, entityID, commandValue); err != nil {
			mh.logger.WithError(err).WithField("entity_id", entityID).Error("Failed to publish command state")
		}
	}
}

// sendMIDICommand sends a MIDI command to the device
func (mh *MIDIHandler) sendMIDICommand(device *MIDIDevice, deviceID, entityID string, entity *HAEntity, command string) bool {
	// Parse entity ID to determine MIDI message type
	parts := strings.Split(entityID, "_")
	if len(parts) < 2 {
		mh.logger.WithFields(logrus.Fields{
			"entity_id": entityID,
			"device":    device.DeviceName,
		}).Error("Invalid entity ID format")
		return false
	}

	mh.logger.WithFields(logrus.Fields{
		"entity_id": entityID,
		"parts":     parts,
		"command":   command,
		"device":    device.DeviceName,
	}).Info("Processing MIDI command")

	// Handle different entity types
	switch parts[0] {
	case "button":
		if parts[1] == "light" {
			return mh.sendButtonLightCommand(device, parts, command)
		} else {
			mh.logger.WithFields(logrus.Fields{
				"entity_id": entityID,
				"device":    device.DeviceName,
			}).Warn("Unknown button entity type")
			return false
		}
	case "cc":
		return mh.sendControlChangeCommand(device, parts, command)
	case "pc":
		return mh.sendProgramChangeCommand(device, parts, command)
	case "pb":
		return mh.sendPitchBendCommand(device, parts, command)
	default:
		// Legacy support for old entity IDs
		return mh.sendLegacyCommand(device, parts, command)
	}
}

// parseCommandValue converts command string to appropriate value type
func parseCommandValue(command string) interface{} {
	if command == "ON" {
		return true
	}
	if command == "OFF" {
		return false
	}
	if val, err := strconv.Atoi(command); err == nil {
		return val
	}
	return command
}

// sendButtonLightCommand sends a command to control button light
func (mh *MIDIHandler) sendButtonLightCommand(device *MIDIDevice, parts []string, command string) bool {
	if len(parts) < 4 {
		mh.logger.WithFields(logrus.Fields{
			"parts":        parts,
			"expected_len": 4,
			"actual_len":   len(parts),
			"device":       device.DeviceName,
		}).Error("Invalid button light entity ID format")
		return false
	}

	key, err := strconv.Atoi(parts[2])
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"key_part": parts[2],
			"device":   device.DeviceName,
		}).Error("Failed to parse key from entity ID")
		return false
	}

	channel, err := strconv.Atoi(parts[3])
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"channel_part": parts[3],
			"device":       device.DeviceName,
		}).Error("Failed to parse channel from entity ID")
		return false
	}

	// Try to find the appropriate output port for this device
	out, err := mh.findOutputPortForDevice(device)
	if err != nil {
		mh.logger.WithError(err).WithField("device", device.DeviceName).Warn("No MIDI output ports available for sending light commands")
		return false
	}

	senderKey := fmt.Sprintf("light_%s_%s", device.ID, out.String())
	send, err := mh.createManagedSender(out, senderKey)
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"device":      device.DeviceName,
			"output_port": out.String(),
		}).Error("Failed to create MIDI sender for light command")
		return false
	}

	// Send note on/off to control button light
	if command == "ON" {
		msg := midi.NoteOn(uint8(channel), uint8(key), 127) // Full velocity for light
		mh.logger.WithFields(logrus.Fields{
			"channel":  channel,
			"key":      key,
			"command":  command,
			"type":     "note_on",
			"velocity": 127,
			"device":   device.DeviceName,
		}).Info("Sending button light ON command")

		err = send(msg)
		if err != nil {
			mh.logger.WithError(err).WithFields(logrus.Fields{
				"channel":     channel,
				"key":         key,
				"command":     command,
				"device":      device.DeviceName,
				"output_port": out.String(),
			}).Error("Failed to send button light MIDI command")
			return false
		}
	} else if command == "OFF" {
		return mh.sendLightOffCommand(device, send, uint8(channel), uint8(key))
	} else {
		mh.logger.WithFields(logrus.Fields{
			"command": command,
			"channel": channel,
			"key":     key,
			"device":  device.DeviceName,
		}).Warn("Unknown light command, ignoring")
		return false
	}

	mh.logger.WithFields(logrus.Fields{
		"channel":     channel,
		"key":         key,
		"command":     command,
		"device":      device.DeviceName,
		"output_port": out.String(),
	}).Info("Successfully sent button light command")

	return true
}

// sendLightOffCommand sends the appropriate light off command based on configuration
func (mh *MIDIHandler) sendLightOffCommand(device *MIDIDevice, send func(midi.Message) error, channel, key uint8) bool {
	method := mh.config.GetDeviceLightOffMethod(device.DeviceName)

	mh.logger.WithFields(logrus.Fields{
		"channel": channel,
		"key":     key,
		"method":  method,
		"device":  device.DeviceName,
	}).Info("Sending button light OFF command")

	switch method {
	case "note_off":
		msg := midi.NoteOff(channel, key)
		err := send(msg)
		if err != nil {
			mh.logger.WithError(err).WithFields(logrus.Fields{
				"channel": channel,
				"key":     key,
				"method":  method,
				"device":  device.DeviceName,
			}).Error("Failed to send NoteOff command")
			return false
		}
		mh.logger.WithFields(logrus.Fields{
			"channel": channel,
			"key":     key,
			"type":    "note_off",
			"device":  device.DeviceName,
		}).Info("Sent NoteOff command")

	case "velocity_zero":
		msg := midi.NoteOn(channel, key, 0)
		err := send(msg)
		if err != nil {
			mh.logger.WithError(err).WithFields(logrus.Fields{
				"channel": channel,
				"key":     key,
				"method":  method,
				"device":  device.DeviceName,
			}).Error("Failed to send NoteOn velocity 0 command")
			return false
		}
		mh.logger.WithFields(logrus.Fields{
			"channel": channel,
			"key":     key,
			"type":    "note_on_vel0",
			"device":  device.DeviceName,
		}).Info("Sent NoteOn velocity 0 command")

	case "velocity_one":
		msg := midi.NoteOn(channel, key, 1)
		err := send(msg)
		if err != nil {
			mh.logger.WithError(err).WithFields(logrus.Fields{
				"channel": channel,
				"key":     key,
				"method":  method,
				"device":  device.DeviceName,
			}).Error("Failed to send NoteOn velocity 1 command")
			return false
		}
		mh.logger.WithFields(logrus.Fields{
			"channel": channel,
			"key":     key,
			"type":    "note_on_vel1",
			"device":  device.DeviceName,
		}).Info("Sent NoteOn velocity 1 command")

	case "multi":
		// Try multiple methods as different devices handle this differently
		var errors []error

		// Method 1: NoteOff (standard MIDI)
		msg1 := midi.NoteOff(channel, key)
		err1 := send(msg1)
		if err1 != nil {
			errors = append(errors, err1)
		}

		// Method 2: NoteOn with velocity 0 (alternative method many devices use)
		msg2 := midi.NoteOn(channel, key, 0)
		err2 := send(msg2)
		if err2 != nil {
			errors = append(errors, err2)
		}

		// Method 3: NoteOn with low velocity (some devices need this)
		msg3 := midi.NoteOn(channel, key, 1)
		err3 := send(msg3)
		if err3 != nil {
			errors = append(errors, err3)
		}

		// Method 4: Another NoteOff for devices that need it repeated
		msg4 := midi.NoteOff(channel, key)
		err4 := send(msg4)
		if err4 != nil {
			errors = append(errors, err4)
		}

		mh.logger.WithFields(logrus.Fields{
			"channel":            channel,
			"key":                key,
			"type":               "multi_method_off",
			"device":             device.DeviceName,
			"note_off_1_error":   err1,
			"note_on_vel0_error": err2,
			"note_on_vel1_error": err3,
			"note_off_2_error":   err4,
		}).Info("Sent multiple light OFF methods")

		// Consider it successful if any method worked
		if len(errors) == 4 {
			mh.logger.WithFields(logrus.Fields{
				"channel": channel,
				"key":     key,
				"device":  device.DeviceName,
				"errors":  errors,
			}).Error("All light OFF methods failed")
			return false
		}

	default:
		mh.logger.WithFields(logrus.Fields{
			"method":  method,
			"channel": channel,
			"key":     key,
			"device":  device.DeviceName,
		}).Error("Unknown light off method configured")
		return false
	}

	return true
}

// sendControlChangeCommand sends a control change command
func (mh *MIDIHandler) sendControlChangeCommand(device *MIDIDevice, parts []string, command string) bool {
	if len(parts) < 3 {
		mh.logger.WithFields(logrus.Fields{
			"parts":  parts,
			"device": device.DeviceName,
		}).Error("Invalid control change entity ID format")
		return false
	}

	controller, err := strconv.Atoi(parts[1])
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"controller_part": parts[1],
			"device":          device.DeviceName,
		}).Error("Failed to parse controller from entity ID")
		return false
	}

	channel, err := strconv.Atoi(parts[2])
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"channel_part": parts[2],
			"device":       device.DeviceName,
		}).Error("Failed to parse channel from entity ID")
		return false
	}

	value, err := strconv.Atoi(command)
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"command": command,
			"device":  device.DeviceName,
		}).Error("Failed to parse value from command")
		return false
	}

	// Clamp value to MIDI range
	if value < 0 {
		value = 0
	}
	if value > 127 {
		value = 127
	}

	// Try to find the appropriate output port for this device
	out, err := mh.findOutputPortForDevice(device)
	if err != nil {
		mh.logger.WithError(err).WithField("device", device.DeviceName).Warn("No MIDI output ports available for sending control change commands")
		return false
	}

	senderKey := fmt.Sprintf("cc_%s_%s", device.ID, out.String())
	send, err := mh.createManagedSender(out, senderKey)
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"device":      device.DeviceName,
			"output_port": out.String(),
		}).Error("Failed to create MIDI sender for control change command")
		return false
	}

	msg := midi.ControlChange(uint8(channel), uint8(controller), uint8(value))
	err = send(msg)
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"channel":     channel,
			"controller":  controller,
			"value":       value,
			"device":      device.DeviceName,
			"output_port": out.String(),
		}).Error("Failed to send control change MIDI command")
		return false
	}

	mh.logger.WithFields(logrus.Fields{
		"channel":     channel,
		"controller":  controller,
		"value":       value,
		"device":      device.DeviceName,
		"output_port": out.String(),
	}).Info("Successfully sent control change command")

	return true
}

// sendProgramChangeCommand sends a program change command
func (mh *MIDIHandler) sendProgramChangeCommand(device *MIDIDevice, parts []string, command string) bool {
	if len(parts) < 2 {
		mh.logger.WithFields(logrus.Fields{
			"parts":  parts,
			"device": device.DeviceName,
		}).Error("Invalid program change entity ID format")
		return false
	}

	channel, err := strconv.Atoi(parts[1])
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"channel_part": parts[1],
			"device":       device.DeviceName,
		}).Error("Failed to parse channel from entity ID")
		return false
	}

	program, err := strconv.Atoi(command)
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"command": command,
			"device":  device.DeviceName,
		}).Error("Failed to parse value from command")
		return false
	}

	// Clamp program to MIDI range
	if program < 0 {
		program = 0
	}
	if program > 127 {
		program = 127
	}

	// Try to find the appropriate output port for this device
	out, err := mh.findOutputPortForDevice(device)
	if err != nil {
		mh.logger.WithError(err).WithField("device", device.DeviceName).Warn("No MIDI output ports available for sending program change commands")
		return false
	}

	senderKey := fmt.Sprintf("pc_%s_%s", device.ID, out.String())
	send, err := mh.createManagedSender(out, senderKey)
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"device":      device.DeviceName,
			"output_port": out.String(),
		}).Error("Failed to create MIDI sender for program change command")
		return false
	}

	msg := midi.ProgramChange(uint8(channel), uint8(program))
	err = send(msg)
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"channel":     channel,
			"program":     program,
			"device":      device.DeviceName,
			"output_port": out.String(),
		}).Error("Failed to send program change MIDI command")
		return false
	}

	mh.logger.WithFields(logrus.Fields{
		"channel":     channel,
		"program":     program,
		"device":      device.DeviceName,
		"output_port": out.String(),
	}).Info("Successfully sent program change command")

	return true
}

// sendPitchBendCommand sends a pitch bend command
func (mh *MIDIHandler) sendPitchBendCommand(device *MIDIDevice, parts []string, command string) bool {
	if len(parts) < 3 {
		mh.logger.WithFields(logrus.Fields{
			"parts":  parts,
			"device": device.DeviceName,
		}).Error("Invalid pitch bend entity ID format")
		return false
	}

	channel, err := strconv.Atoi(parts[1])
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"channel_part": parts[1],
			"device":       device.DeviceName,
		}).Error("Failed to parse channel from entity ID")
		return false
	}

	percentage, err := strconv.Atoi(command)
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"command": command,
			"device":  device.DeviceName,
		}).Error("Failed to parse percentage from command")
		return false
	}

	// Clamp percentage to 0-100 range
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 100 {
		percentage = 100
	}

	// Convert percentage (0-100) to MIDI pitch bend range (-8192 to +configured_max)
	// 0% = -8192 (min), 50% = 0 (center), 100% = configured_max (max)
	maxValue := mh.config.MIDI.PitchBendMaxValue
	var pitchBend int
	if percentage >= 100 {
		// Ensure 100% always maps to maximum pitch bend
		pitchBend = int(maxValue)
	} else {
		totalRange := float64(maxValue + 8192) // Total range from -8192 to +maxValue
		pitchBend = int(float64(percentage)/100.0*totalRange) - 8192
	}

	// Clamp to MIDI range (safety check)
	if pitchBend < -8192 {
		pitchBend = -8192
	}
	if pitchBend > 8191 {
		pitchBend = 8191
	}

	// Try to find the appropriate output port for this device
	out, err := mh.findOutputPortForDevice(device)
	if err != nil {
		mh.logger.WithError(err).WithField("device", device.DeviceName).Warn("No MIDI output ports available for sending pitch bend commands")
		return false
	}

	senderKey := fmt.Sprintf("pb_%s_%s", device.ID, out.String())
	send, err := mh.createManagedSender(out, senderKey)
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"device":      device.DeviceName,
			"output_port": out.String(),
		}).Error("Failed to create MIDI sender for pitch bend command")
		return false
	}

	msg := midi.Pitchbend(uint8(channel), int16(pitchBend))
	err = send(msg)
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"channel":     channel,
			"percentage":  percentage,
			"pitch_bend":  pitchBend,
			"device":      device.DeviceName,
			"output_port": out.String(),
		}).Error("Failed to send pitch bend MIDI command")
		return false
	}

	mh.logger.WithFields(logrus.Fields{
		"channel":     channel,
		"percentage":  percentage,
		"pitch_bend":  pitchBend,
		"device":      device.DeviceName,
		"output_port": out.String(),
	}).Info("Successfully sent pitch bend command")

	return true
}

// sendLegacyCommand handles legacy entity IDs for backwards compatibility
func (mh *MIDIHandler) sendLegacyCommand(device *MIDIDevice, parts []string, command string) bool {
	if len(parts) < 2 {
		mh.logger.WithFields(logrus.Fields{
			"parts":  parts,
			"device": device.DeviceName,
		}).Error("Invalid legacy entity ID format")
		return false
	}

	msgType := parts[0]

	switch msgType {
	case "note":
		return mh.sendNoteCommand(parts, command)
	case "cc":
		return mh.sendControlChangeCommand(device, parts, command)
	case "pc":
		return mh.sendProgramChangeCommand(device, parts, command)
	default:
		mh.logger.WithFields(logrus.Fields{
			"msg_type": msgType,
			"device":   device.DeviceName,
		}).Warn("Unknown legacy message type, ignoring")
		return false
	}
}

// sendNoteCommand sends a note on/off command (legacy support)
func (mh *MIDIHandler) sendNoteCommand(parts []string, command string) bool {
	if len(parts) < 3 {
		mh.logger.WithField("parts", parts).Error("Invalid note entity ID format")
		return false
	}

	key, err := strconv.Atoi(parts[1])
	if err != nil {
		mh.logger.WithError(err).WithField("key_part", parts[1]).Error("Failed to parse key from entity ID")
		return false
	}

	channel, err := strconv.Atoi(parts[2])
	if err != nil {
		mh.logger.WithError(err).WithField("channel_part", parts[2]).Error("Failed to parse channel from entity ID")
		return false
	}

	// Try to find an output port for sending commands
	outPorts := mh.getCachedOutPorts()
	if len(outPorts) == 0 {
		mh.logger.Warn("No MIDI output ports available for sending note commands")
		return false
	}

	// Use the first available output port
	out := outPorts[0]
	senderKey := fmt.Sprintf("note_%s_%s", "global", out.String())
	send, err := mh.createManagedSender(out, senderKey)
	if err != nil {
		mh.logger.WithError(err).Error("Failed to create MIDI sender for note command")
		return false
	}

	var msg midi.Message
	if command == "ON" {
		msg = midi.NoteOn(uint8(channel), uint8(key), 100) // velocity 100
		mh.logger.WithFields(logrus.Fields{
			"channel": channel,
			"key":     key,
			"command": command,
			"type":    "note_on",
		}).Info("Sending note ON command")
	} else if command == "OFF" {
		msg = midi.NoteOff(uint8(channel), uint8(key))
		mh.logger.WithFields(logrus.Fields{
			"channel": channel,
			"key":     key,
			"command": command,
			"type":    "note_off",
		}).Info("Sending note OFF command")
	} else {
		mh.logger.WithFields(logrus.Fields{
			"command": command,
			"channel": channel,
			"key":     key,
		}).Warn("Unknown note command, ignoring")
		return false
	}

	err = send(msg)
	if err != nil {
		mh.logger.WithError(err).WithFields(logrus.Fields{
			"channel": channel,
			"key":     key,
			"command": command,
		}).Error("Failed to send note MIDI command")
		return false
	}

	mh.logger.WithFields(logrus.Fields{
		"channel": channel,
		"key":     key,
		"command": command,
	}).Info("Successfully sent note command")

	return true
}

// findOutputPortForDevice tries to find the best output port for a given device
func (mh *MIDIHandler) findOutputPortForDevice(device *MIDIDevice) (drivers.Out, error) {
	// Use cached output ports to reduce ALSA resource creation
	outPorts := mh.getCachedOutPorts()
	if len(outPorts) == 0 {
		return nil, fmt.Errorf("no MIDI output ports available")
	}

	// First, try to find an exact match by device name
	for _, out := range outPorts {
		if out.String() == device.DeviceName {
			mh.logger.WithFields(logrus.Fields{
				"device":      device.DeviceName,
				"output_port": out.String(),
			}).Info("Found matching output port for device")
			return out, nil
		}
	}

	// If no exact match, try to find a partial match (contains device name or vice versa)
	for _, out := range outPorts {
		outName := out.String()
		// Check if device name contains output port name or vice versa
		if strings.Contains(strings.ToLower(device.DeviceName), strings.ToLower(outName)) ||
			strings.Contains(strings.ToLower(outName), strings.ToLower(device.DeviceName)) {
			mh.logger.WithFields(logrus.Fields{
				"device":      device.DeviceName,
				"output_port": outName,
			}).Info("Found partial matching output port for device")
			return out, nil
		}
	}

	// Fall back to first available port
	mh.logger.WithFields(logrus.Fields{
		"device":      device.DeviceName,
		"output_port": outPorts[0].String(),
		"reason":      "no matching port found, using first available",
	}).Warn("Using first available output port for device")

	return outPorts[0], nil
}
