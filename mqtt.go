package main

import (
	"fmt"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/sirupsen/logrus"
)

// MQTTClient handles MQTT communication with Home Assistant
type MQTTClient struct {
	client    mqtt.Client
	config    *Config
	logger    *logrus.Logger
	onMessage func(topic string, payload []byte)
}

// NewMQTTClient creates a new MQTT client
func NewMQTTClient(config *Config, logger *logrus.Logger, onMessage func(topic string, payload []byte)) *MQTTClient {
	return &MQTTClient{
		config:    config,
		logger:    logger,
		onMessage: onMessage,
	}
}

// Connect establishes connection to MQTT broker
func (m *MQTTClient) Connect() error {
	opts := mqtt.NewClientOptions()

	brokerURL := fmt.Sprintf("tcp://%s:%d", m.config.MQTT.Broker, m.config.MQTT.Port)
	opts.AddBroker(brokerURL)
	opts.SetClientID(m.config.MQTT.ClientID)

	if m.config.MQTT.Username != "" {
		opts.SetUsername(m.config.MQTT.Username)
	}
	if m.config.MQTT.Password != "" {
		opts.SetPassword(m.config.MQTT.Password)
	}

	opts.SetKeepAlive(60 * time.Second)
	opts.SetPingTimeout(1 * time.Second)
	opts.SetConnectTimeout(5 * time.Second)
	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(10 * time.Second)

	opts.SetOnConnectHandler(func(c mqtt.Client) {
		m.logger.Info("Connected to MQTT broker")
	})

	opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		m.logger.WithError(err).Error("Lost connection to MQTT broker")
	})

	opts.SetReconnectingHandler(func(c mqtt.Client, opts *mqtt.ClientOptions) {
		m.logger.Info("Reconnecting to MQTT broker...")
	})

	// Set default message handler
	opts.SetDefaultPublishHandler(func(client mqtt.Client, msg mqtt.Message) {
		if m.onMessage != nil {
			m.onMessage(msg.Topic(), msg.Payload())
		}
	})

	m.client = mqtt.NewClient(opts)

	if token := m.client.Connect(); token.Wait() && token.Error() != nil {
		return fmt.Errorf("failed to connect to MQTT broker: %v", token.Error())
	}

	return nil
}

// Disconnect closes the MQTT connection
func (m *MQTTClient) Disconnect() {
	if m.client != nil && m.client.IsConnected() {
		m.client.Disconnect(250)
	}
}

// PublishDiscovery publishes device discovery information to Home Assistant
func (m *MQTTClient) PublishDiscovery(device *MIDIDevice) error {
	for entityID := range device.Entities {
		entityType := device.GetEntityType(entityID)
		topic := device.GetDiscoveryTopic(entityID, entityType, m.config.MQTT.DiscoveryPrefix, m.config.Bridge.ID)

		payload, err := device.GetDiscoveryPayload(entityID)
		if err != nil {
			return fmt.Errorf("failed to get discovery payload for %s: %v", entityID, err)
		}

		if err := m.Publish(topic, payload, true); err != nil {
			return fmt.Errorf("failed to publish discovery for %s: %v", entityID, err)
		}

		m.logger.WithFields(logrus.Fields{
			"entity_id": entityID,
			"topic":     topic,
			"type":      entityType,
		}).Info("Published entity discovery")
	}

	return nil
}

// Publish publishes a message to the specified topic
func (m *MQTTClient) Publish(topic string, payload []byte, retain bool) error {
	if m.client == nil || !m.client.IsConnected() {
		m.logger.WithField("topic", topic).Warn("MQTT client not connected, cannot publish")
		return fmt.Errorf("MQTT client not connected")
	}

	token := m.client.Publish(topic, 0, retain, payload)
	if token.Wait() && token.Error() != nil {
		m.logger.WithFields(logrus.Fields{
			"topic": topic,
			"error": token.Error(),
		}).Error("Failed to publish MQTT message")
		return fmt.Errorf("failed to publish to %s: %v", topic, token.Error())
	}

	m.logger.WithFields(logrus.Fields{
		"topic":        topic,
		"payload_size": len(payload),
		"retain":       retain,
	}).Debug("Successfully published MQTT message")

	return nil
}

// PublishState publishes entity state to Home Assistant
func (m *MQTTClient) PublishState(device *MIDIDevice, entityID string, state interface{}) error {
	entity, exists := device.Entities[entityID]
	if !exists {
		return fmt.Errorf("entity %s not found", entityID)
	}

	var payload string
	switch v := state.(type) {
	case bool:
		if v {
			payload = entity.PayloadOn
			if payload == "" {
				payload = "ON"
			}
		} else {
			payload = entity.PayloadOff
			if payload == "" {
				payload = "OFF"
			}
		}
	case int:
		payload = fmt.Sprintf("%d", v)
	case float64:
		payload = fmt.Sprintf("%.2f", v)
	case string:
		payload = v
	default:
		payload = fmt.Sprintf("%v", v)
	}

	err := m.Publish(entity.StateTopic, []byte(payload), true)
	if err != nil {
		return fmt.Errorf("failed to publish state for %s: %v", entityID, err)
	}

	entityType := device.GetEntityType(entityID)
	m.logger.WithFields(logrus.Fields{
		"entity_id":   entityID,
		"entity_type": entityType,
		"state":       payload,
	}).Debug("Published entity state")

	return nil
}

// Subscribe subscribes to entity command topics (only for controllable entities)
func (m *MQTTClient) Subscribe(device *MIDIDevice) error {
	// Subscribe to all command topics for this device using wildcard
	deviceCommandTopic := fmt.Sprintf("ha-midi/%s/%s/+/set", m.config.Bridge.ID, device.ID)

	token := m.client.Subscribe(deviceCommandTopic, 0, nil)
	if token.Wait() && token.Error() != nil {
		return fmt.Errorf("failed to subscribe to device command topic %s: %v", deviceCommandTopic, token.Error())
	}

	m.logger.WithFields(logrus.Fields{
		"device_id": device.ID,
		"bridge_id": m.config.Bridge.ID,
		"topic":     deviceCommandTopic,
	}).Info("Subscribed to device command topic (all entities)")

	return nil
}

// RemoveDiscovery removes device discovery information from Home Assistant
func (m *MQTTClient) RemoveDiscovery(device *MIDIDevice) error {
	for entityID := range device.Entities {
		entityType := device.GetEntityType(entityID)
		topic := device.GetDiscoveryTopic(entityID, entityType, m.config.MQTT.DiscoveryPrefix, m.config.Bridge.ID)

		// Publishing empty payload with retain=true removes the discovery
		if err := m.Publish(topic, []byte(""), true); err != nil {
			return fmt.Errorf("failed to remove discovery for %s: %v", entityID, err)
		}

		m.logger.WithFields(logrus.Fields{
			"entity_id":   entityID,
			"entity_type": entityType,
			"topic":       topic,
		}).Info("Removed entity discovery")
	}

	return nil
}

// IsConnected returns true if the MQTT client is connected
func (m *MQTTClient) IsConnected() bool {
	return m.client != nil && m.client.IsConnected()
}

// PublishAvailability publishes device availability status
func (m *MQTTClient) PublishAvailability(device *MIDIDevice, available bool) error {
	if device.AvailabilityTopic == "" {
		return nil // No availability topic configured
	}

	var payload string
	if available {
		payload = "online"
	} else {
		payload = "offline"
	}

	err := m.Publish(device.AvailabilityTopic, []byte(payload), true)
	if err != nil {
		return fmt.Errorf("failed to publish availability for device %s: %v", device.ID, err)
	}

	status := "offline"
	if available {
		status = "online"
	}

	m.logger.WithFields(logrus.Fields{
		"device_id": device.ID,
		"status":    status,
		"topic":     device.AvailabilityTopic,
	}).Info("Published device availability")

	return nil
}

// SetDeviceOffline publishes offline status for a device (better than removing discovery)
func (m *MQTTClient) SetDeviceOffline(device *MIDIDevice) error {
	return m.PublishAvailability(device, false)
}

// SetDeviceOnline publishes online status for a device
func (m *MQTTClient) SetDeviceOnline(device *MIDIDevice) error {
	return m.PublishAvailability(device, true)
}
