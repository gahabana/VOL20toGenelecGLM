// Package mqtt provides an MQTT client for Home Assistant integration.
//
// Publishes GLM state and subscribes to control commands.
// Supports Home Assistant MQTT Discovery for automatic entity creation.
package mqtt

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"

	"vol20toglm/controller"
	"vol20toglm/types"
)

const haDiscoveryPrefix = "homeassistant"

// Client is an MQTT client for GLM control and state publishing.
type Client struct {
	client    pahomqtt.Client
	ctrl      *controller.Controller
	actions   chan<- types.Action
	traceGen  *types.TraceIDGenerator
	log       *slog.Logger
	topicPfx  string
	haDiscov  bool
}

// statePayload is the JSON published to the state topic.
type statePayload struct {
	Volume   int    `json:"volume"`
	VolumeDB int    `json:"volume_db"`
	Mute     string `json:"mute"`
	Dim      string `json:"dim"`
	Power    string `json:"power"`
}

func boolToOnOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

// Start creates and connects the MQTT client. Returns nil if broker is empty.
func Start(
	broker string,
	port int,
	username, password string,
	topicPrefix string,
	haDiscovery bool,
	ctrl *controller.Controller,
	actions chan<- types.Action,
	traceGen *types.TraceIDGenerator,
	log *slog.Logger,
) *Client {
	if broker == "" {
		return nil
	}

	c := &Client{
		ctrl:     ctrl,
		actions:  actions,
		traceGen: traceGen,
		log:      log,
		topicPfx: topicPrefix,
		haDiscov: haDiscovery,
	}

	opts := pahomqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", broker, port))
	opts.SetClientID("vol20toglm")
	opts.SetAutoReconnect(true)
	opts.SetWill(topicPrefix+"/availability", "offline", 0, true)
	opts.SetOnConnectHandler(c.onConnect)
	opts.SetConnectionLostHandler(c.onConnectionLost)

	if username != "" {
		opts.SetUsername(username)
		opts.SetPassword(password)
	}

	c.client = pahomqtt.NewClient(opts)

	token := c.client.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		log.Error("MQTT connect failed", "broker", broker, "port", port, "err", err)
		return nil
	}

	log.Info("MQTT client started", "broker", broker, "port", port, "topic", topicPrefix)

	// Register state callback so changes are published automatically
	ctrl.OnStateChange(func(_, new_ types.State) {
		c.publishState(new_)
	})

	return c
}

// Stop disconnects the client gracefully.
func (c *Client) Stop() {
	if c == nil || c.client == nil {
		return
	}
	c.client.Publish(c.topicPfx+"/availability", 0, true, "offline")
	c.client.Disconnect(1000)
	c.log.Info("MQTT client stopped")
}

func (c *Client) onConnect(client pahomqtt.Client) {
	c.log.Info("MQTT connected")

	// Subscribe to command topics
	cmdTopics := map[string]pahomqtt.MessageHandler{
		c.topicPfx + "/set/volume": c.handleVolume,
		c.topicPfx + "/set/mute":   c.handleMute,
		c.topicPfx + "/set/dim":    c.handleDim,
		c.topicPfx + "/set/power":  c.handlePower,
	}
	for topic, handler := range cmdTopics {
		client.Subscribe(topic, 0, handler)
	}
	c.log.Info("MQTT subscribed", "topics", c.topicPfx+"/set/+")

	// Publish availability
	client.Publish(c.topicPfx+"/availability", 0, true, "online")

	// Publish HA Discovery configs
	if c.haDiscov {
		c.publishHADiscovery()
	}

	// Publish current state
	c.publishState(c.ctrl.GetState())
}

func (c *Client) onConnectionLost(_ pahomqtt.Client, err error) {
	c.log.Warn("MQTT connection lost", "err", err)
}

// Command handlers

func (c *Client) handleVolume(_ pahomqtt.Client, msg pahomqtt.Message) {
	payload := strings.TrimSpace(string(msg.Payload()))
	tid := c.traceGen.Next("mqtt")
	c.log.Debug("MQTT rx", "topic", msg.Topic(), "payload", payload, "trace_id", tid)

	value, err := strconv.Atoi(payload)
	if err != nil {
		c.log.Warn("MQTT invalid volume", "payload", payload, "trace_id", tid, "err", err)
		return
	}
	// Accept dB value (-127 to 0) or raw value (0-127)
	if value <= 0 {
		value = value + 127
	}
	if value < 0 {
		value = 0
	} else if value > 127 {
		value = 127
	}

	c.actions <- types.Action{
		Kind:      types.KindSetVolume,
		Value:     value,
		Source:    "mqtt",
		TraceID:   tid,
		Timestamp: time.Now(),
	}
}

func (c *Client) handleMute(_ pahomqtt.Client, msg pahomqtt.Message) {
	payload := strings.TrimSpace(string(msg.Payload()))
	tid := c.traceGen.Next("mqtt")
	c.log.Debug("MQTT rx", "topic", msg.Topic(), "payload", payload, "trace_id", tid)

	boolVal, toggle, err := parseBoolOrToggle(payload)
	if err != nil {
		c.log.Warn("MQTT invalid mute", "payload", payload, "trace_id", tid, "err", err)
		return
	}

	c.actions <- types.Action{
		Kind:      types.KindSetMute,
		BoolValue: boolVal,
		Toggle:    toggle,
		Source:    "mqtt",
		TraceID:   tid,
		Timestamp: time.Now(),
	}
}

func (c *Client) handleDim(_ pahomqtt.Client, msg pahomqtt.Message) {
	payload := strings.TrimSpace(string(msg.Payload()))
	tid := c.traceGen.Next("mqtt")
	c.log.Debug("MQTT rx", "topic", msg.Topic(), "payload", payload, "trace_id", tid)

	boolVal, toggle, err := parseBoolOrToggle(payload)
	if err != nil {
		c.log.Warn("MQTT invalid dim", "payload", payload, "trace_id", tid, "err", err)
		return
	}

	c.actions <- types.Action{
		Kind:      types.KindSetDim,
		BoolValue: boolVal,
		Toggle:    toggle,
		Source:    "mqtt",
		TraceID:   tid,
		Timestamp: time.Now(),
	}
}

func (c *Client) handlePower(_ pahomqtt.Client, msg pahomqtt.Message) {
	payload := strings.TrimSpace(string(msg.Payload()))
	tid := c.traceGen.Next("mqtt")
	c.log.Debug("MQTT rx", "topic", msg.Topic(), "payload", payload, "trace_id", tid)

	boolVal, toggle, err := parseBoolOrToggle(payload)
	if err != nil {
		c.log.Warn("MQTT invalid power", "payload", payload, "trace_id", tid, "err", err)
		return
	}

	c.actions <- types.Action{
		Kind:      types.KindSetPower,
		BoolValue: boolVal,
		Toggle:    toggle,
		Source:    "mqtt",
		TraceID:   tid,
		Timestamp: time.Now(),
	}
}

// State publishing

func (c *Client) publishState(state types.State) {
	if c.client == nil || !c.client.IsConnected() {
		return
	}

	payload := statePayload{
		Volume:   state.Volume,
		VolumeDB: state.Volume - 127,
		Mute:     boolToOnOff(state.Mute),
		Dim:      boolToOnOff(state.Dim),
		Power:    boolToOnOff(state.Power),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		c.log.Error("MQTT marshal state failed", "err", err)
		return
	}

	c.client.Publish(c.topicPfx+"/state", 0, true, data)
}

// Home Assistant MQTT Discovery

func (c *Client) publishHADiscovery() {
	device := map[string]interface{}{
		"identifiers":  []string{"glm_controller"},
		"name":         "Genelec GLM",
		"manufacturer": "Genelec",
		"model":        "GLM Controller",
	}

	avail := c.topicPfx + "/availability"
	state := c.topicPfx + "/state"

	// Volume as number entity (dB: -127 to 0)
	c.publishDiscoveryConfig("number", "glm_volume", map[string]interface{}{
		"name":                "GLM Volume",
		"unique_id":           "glm_volume",
		"command_topic":       c.topicPfx + "/set/volume",
		"state_topic":         state,
		"value_template":      "{{ value_json.volume_db }}",
		"min":                 -127,
		"max":                 0,
		"step":                1,
		"unit_of_measurement": "dB",
		"icon":                "mdi:volume-high",
		"availability_topic":  avail,
		"device":              device,
	})

	// Mute as switch (config category — keeps device toggle = Power only)
	c.publishDiscoveryConfig("switch", "glm_mute", map[string]interface{}{
		"name":               "GLM Mute",
		"unique_id":          "glm_mute",
		"command_topic":      c.topicPfx + "/set/mute",
		"state_topic":        state,
		"value_template":     "{{ value_json.mute }}",
		"payload_on":         "ON",
		"payload_off":        "OFF",
		"icon":               "mdi:volume-mute",
		"availability_topic": avail,
		"entity_category":    "config",
		"device":             device,
	})

	// Dim as switch (config category — keeps device toggle = Power only)
	c.publishDiscoveryConfig("switch", "glm_dim", map[string]interface{}{
		"name":               "GLM Dim",
		"unique_id":          "glm_dim",
		"command_topic":      c.topicPfx + "/set/dim",
		"state_topic":        state,
		"value_template":     "{{ value_json.dim }}",
		"payload_on":         "ON",
		"payload_off":        "OFF",
		"icon":               "mdi:brightness-6",
		"availability_topic": avail,
		"entity_category":    "config",
		"device":             device,
	})

	// Power as switch
	c.publishDiscoveryConfig("switch", "glm_power", map[string]interface{}{
		"name":               "GLM Power",
		"unique_id":          "glm_power",
		"command_topic":      c.topicPfx + "/set/power",
		"state_topic":        state,
		"value_template":     "{{ value_json.power }}",
		"payload_on":         "ON",
		"payload_off":        "OFF",
		"icon":               "mdi:power",
		"availability_topic": avail,
		"device":             device,
	})

	c.log.Info("MQTT HA Discovery configs published")
}

func (c *Client) publishDiscoveryConfig(component, objectID string, config map[string]interface{}) {
	topic := fmt.Sprintf("%s/%s/%s/config", haDiscoveryPrefix, component, objectID)
	data, err := json.Marshal(config)
	if err != nil {
		c.log.Error("MQTT marshal discovery config failed", "component", component, "err", err)
		return
	}
	c.client.Publish(topic, 0, true, data)
}

// Helpers

func parseBoolOrToggle(payload string) (boolVal bool, toggle bool, err error) {
	switch strings.ToLower(payload) {
	case "on", "true", "1":
		return true, false, nil
	case "off", "false", "0":
		return false, false, nil
	case "toggle":
		return false, true, nil
	default:
		return false, false, fmt.Errorf("unknown state: %s", payload)
	}
}
