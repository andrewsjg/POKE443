package mqtt

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/andrewsjg/simple-healthchecker/copilot/internal/config"
	paho "github.com/eclipse/paho.mqtt.golang"
)

// StateChangeMessage represents a state change notification
type StateChangeMessage struct {
	Timestamp time.Time `json:"timestamp"`
	Host      string    `json:"host"`
	Address   string    `json:"address"`
	CheckType string    `json:"check_type"`
	CheckURL  string    `json:"check_url,omitempty"`
	CheckID   string    `json:"check_id,omitempty"`
	Status    string    `json:"status"` // "up", "down", "blocked"
	LatencyMS int64     `json:"latency_ms,omitempty"`
	Message   string    `json:"message,omitempty"`
}

// Client manages MQTT connections and publishing
type Client struct {
	mu        sync.RWMutex
	settings  config.MQTTSettings
	client    paho.Client
	connected bool
}

// NewClient creates a new MQTT client
func NewClient(settings config.MQTTSettings) *Client {
	return &Client{
		settings: settings,
	}
}

// Connect establishes connection to the MQTT broker
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.settings.Enabled || c.settings.Broker == "" {
		return nil
	}

	opts := paho.NewClientOptions()
	opts.AddBroker(c.settings.Broker)

	clientID := c.settings.ClientID
	if clientID == "" {
		clientID = fmt.Sprintf("healthchecker-%d", time.Now().UnixNano())
	}
	opts.SetClientID(clientID)

	if c.settings.Username != "" {
		opts.SetUsername(c.settings.Username)
		opts.SetPassword(c.settings.Password)
	}

	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(false) // Don't retry on initial connect - we'll handle it ourselves
	opts.SetConnectTimeout(5 * time.Second)
	opts.SetOnConnectHandler(func(client paho.Client) {
		log.Printf("MQTT connected to %s", c.settings.Broker)
		c.mu.Lock()
		c.connected = true
		c.mu.Unlock()
	})
	opts.SetConnectionLostHandler(func(client paho.Client, err error) {
		log.Printf("MQTT connection lost: %v", err)
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
	})

	c.client = paho.NewClient(opts)
	token := c.client.Connect()
	// Wait with timeout to avoid blocking forever
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("MQTT connect timeout after 10s")
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("MQTT connect failed: %w", err)
	}

	c.connected = true
	return nil
}

// Disconnect closes the MQTT connection
func (c *Client) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client != nil && c.connected {
		c.client.Disconnect(1000)
		c.connected = false
	}
}

// UpdateSettings updates the MQTT settings and reconnects if needed
func (c *Client) UpdateSettings(settings config.MQTTSettings) error {
	c.mu.Lock()
	oldSettings := c.settings
	c.settings = settings
	c.mu.Unlock()

	// If settings changed, reconnect
	if oldSettings.Broker != settings.Broker ||
		oldSettings.Username != settings.Username ||
		oldSettings.Password != settings.Password ||
		oldSettings.Enabled != settings.Enabled {
		c.Disconnect()
		if settings.Enabled {
			return c.Connect()
		}
	}
	return nil
}

// PublishStateChange publishes a state change message
func (c *Client) PublishStateChange(msg StateChangeMessage) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.settings.Enabled || c.client == nil || !c.connected {
		return nil
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	baseTopic := c.settings.Topic
	if baseTopic == "" {
		baseTopic = "healthchecker/status"
	}

	// Publish to topic: baseTopic/hostname/checktype or baseTopic/hostname/checkid
	topic := fmt.Sprintf("%s/%s/%s", baseTopic, msg.Host, msg.CheckType)
	if msg.CheckID != "" {
		topic = fmt.Sprintf("%s/%s/%s", baseTopic, msg.Host, msg.CheckID)
	}

	token := c.client.Publish(topic, 0, false, payload)
	// Wait with timeout to avoid blocking
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("MQTT publish timeout")
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("MQTT publish failed: %w", err)
	}

	log.Printf("MQTT published to %s: %s", topic, msg.Status)
	return nil
}

// IsConnected returns whether the client is connected
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// GetSettings returns the current MQTT settings
func (c *Client) GetSettings() config.MQTTSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings
}
