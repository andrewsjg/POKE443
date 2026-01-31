package pushover

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/andrewsjg/simple-healthchecker/copilot/internal/config"
)

// Priority levels for Pushover notifications
const (
	PriorityLowest    = -2
	PriorityLow       = -1
	PriorityNormal    = 0
	PriorityHigh      = 1
	PriorityEmergency = 2
)

// AlertMessage represents a notification to be sent
type AlertMessage struct {
	Host      string
	Address   string
	CheckType string
	CheckID   string
	Status    string // "up", "down"
	Message   string
	LatencyMS int64
}

// Client manages Pushover notifications
type Client struct {
	mu       sync.RWMutex
	settings config.PushoverSettings
	http     *http.Client
}

// NewClient creates a new Pushover client
func NewClient(settings config.PushoverSettings) *Client {
	return &Client{
		settings: settings,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// UpdateSettings updates the Pushover settings
func (c *Client) UpdateSettings(settings config.PushoverSettings) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings = settings
}

// IsEnabled returns whether Pushover notifications are enabled
func (c *Client) IsEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings.Enabled && c.settings.UserKey != "" && c.settings.APIToken != ""
}

// SendAlert sends a notification via Pushover
func (c *Client) SendAlert(msg AlertMessage) error {
	c.mu.RLock()
	settings := c.settings
	c.mu.RUnlock()

	if !settings.Enabled || settings.UserKey == "" || settings.APIToken == "" {
		return nil
	}

	// Build the notification message
	title := fmt.Sprintf("🔴 %s is DOWN", msg.Host)
	priority := PriorityHigh
	sound := "falling"

	if msg.Status == "up" {
		title = fmt.Sprintf("✅ %s is UP", msg.Host)
		priority = PriorityNormal
		sound = "pushover"
	}

	body := fmt.Sprintf("Host: %s (%s)\nCheck: %s", msg.Host, msg.Address, strings.ToUpper(msg.CheckType))
	if msg.CheckID != "" {
		body += fmt.Sprintf(" [%s]", msg.CheckID)
	}
	if msg.Message != "" {
		body += fmt.Sprintf("\n%s", msg.Message)
	}
	if msg.Status == "up" && msg.LatencyMS > 0 {
		body += fmt.Sprintf("\nLatency: %dms", msg.LatencyMS)
	}

	// Override sound if configured
	if settings.Sound != "" {
		sound = settings.Sound
	}

	// Build form data
	data := url.Values{
		"token":    {settings.APIToken},
		"user":     {settings.UserKey},
		"title":    {title},
		"message":  {body},
		"priority": {fmt.Sprintf("%d", priority)},
		"sound":    {sound},
	}

	// Add device if specified
	if settings.Device != "" {
		data.Set("device", settings.Device)
	}

	// For emergency priority, add retry and expire parameters
	if priority == PriorityEmergency {
		data.Set("retry", "60")   // Retry every 60 seconds
		data.Set("expire", "600") // Expire after 10 minutes
	}

	// Send the request
	resp, err := c.http.PostForm("https://api.pushover.net/1/messages.json", data)
	if err != nil {
		return fmt.Errorf("pushover request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pushover returned status %d", resp.StatusCode)
	}

	log.Printf("Pushover notification sent: %s - %s", msg.Host, msg.Status)
	return nil
}

// TestNotification sends a test notification
func (c *Client) TestNotification() error {
	c.mu.RLock()
	settings := c.settings
	c.mu.RUnlock()

	if settings.UserKey == "" || settings.APIToken == "" {
		return fmt.Errorf("pushover credentials not configured")
	}

	data := url.Values{
		"token":    {settings.APIToken},
		"user":     {settings.UserKey},
		"title":    {"POKE443 Test Notification"},
		"message":  {"This is a test notification from POKE443. If you see this, Pushover is configured correctly!"},
		"priority": {fmt.Sprintf("%d", PriorityNormal)},
		"sound":    {"pushover"},
	}

	if settings.Device != "" {
		data.Set("device", settings.Device)
	}

	resp, err := c.http.PostForm("https://api.pushover.net/1/messages.json", data)
	if err != nil {
		return fmt.Errorf("pushover request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pushover returned status %d", resp.StatusCode)
	}

	return nil
}
