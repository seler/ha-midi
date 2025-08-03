package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

// Application represents the main application
type Application struct {
	config      *Config
	logger      *logrus.Logger
	mqttClient  *MQTTClient
	midiHandler *MIDIHandler
	ctx         context.Context
	cancel      context.CancelFunc
}

func main() {
	app := &Application{}

	if err := app.initialize(); err != nil {
		logrus.WithError(err).Fatal("Failed to initialize application")
	}

	if err := app.run(); err != nil {
		logrus.WithError(err).Fatal("Application run failed")
	}
}

func (app *Application) initialize() error {
	// Load configuration
	config, err := LoadConfig("config.yaml")
	if err != nil {
		return err
	}
	app.config = config

	// Setup logger
	app.logger = logrus.New()
	app.logger.SetLevel(parseLogLevel(config.Logging.Level))
	if config.Logging.Format == "json" {
		app.logger.SetFormatter(&logrus.JSONFormatter{})
	} else {
		app.logger.SetFormatter(&logrus.TextFormatter{
			FullTimestamp: true,
		})
	}

	app.logger.Info("Starting HA-MIDI Bridge")
	app.logger.WithFields(logrus.Fields{
		"mqtt_broker":      config.MQTT.Broker,
		"mqtt_port":        config.MQTT.Port,
		"mqtt_client_id":   config.MQTT.ClientID,
		"discovery_prefix": config.MQTT.DiscoveryPrefix,
	}).Info("Loaded configuration")

	// Create context for graceful shutdown
	app.ctx, app.cancel = context.WithCancel(context.Background())

	// Initialize MQTT client
	app.mqttClient = NewMQTTClient(config, app.logger, app.onMQTTMessage)

	// Initialize MIDI handler
	app.midiHandler = NewMIDIHandler(config, app.logger, app.mqttClient)

	return nil
}

func (app *Application) run() error {
	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Connect to MQTT broker
	app.logger.Info("Connecting to MQTT broker...")
	if err := app.mqttClient.Connect(); err != nil {
		return err
	}
	defer app.mqttClient.Disconnect()

	// Start MIDI handling
	app.logger.Info("Starting MIDI device monitoring...")
	if err := app.midiHandler.Start(); err != nil {
		return err
	}
	defer app.midiHandler.Stop()

	app.logger.Info("HA-MIDI Bridge is running")
	app.logger.Info("Press Ctrl+C to exit")

	// Main loop
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-app.ctx.Done():
			return nil

		case sig := <-sigChan:
			app.logger.WithField("signal", sig).Info("Received shutdown signal")
			app.cancel()
			return nil

		case <-ticker.C:
			// Periodic tasks can be added here if needed
			// For now, the MIDI handler and MQTT client handle their own events
		}
	}
}

func (app *Application) onMQTTMessage(topic string, payload []byte) {
	// Forward MQTT messages to MIDI handler for processing
	app.midiHandler.HandleMQTTMessage(topic, payload)
}

func parseLogLevel(level string) logrus.Level {
	switch strings.ToLower(level) {
	case "trace":
		return logrus.TraceLevel
	case "debug":
		return logrus.DebugLevel
	case "info":
		return logrus.InfoLevel
	case "warning", "warn":
		return logrus.WarnLevel
	case "error":
		return logrus.ErrorLevel
	case "fatal":
		return logrus.FatalLevel
	case "panic":
		return logrus.PanicLevel
	default:
		return logrus.InfoLevel
	}
}
