package telegram

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/andrewsjg/simple-healthchecker/copilot/internal/config"
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

// Client manages Telegram notifications
type Client struct {
	mu       sync.RWMutex
	settings config.TelegramSettings
	http     *http.Client
}

// NewClient creates a new Telegram client
func NewClient(settings config.TelegramSettings) *Client {
	return &Client{
		settings: settings,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// UpdateSettings updates the Telegram settings
func (c *Client) UpdateSettings(settings config.TelegramSettings) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings = settings
}

// IsEnabled returns whether Telegram notifications are enabled
func (c *Client) IsEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings.Enabled && c.settings.BotToken != "" && c.settings.ChatID != ""
}

// SendAlert sends a notification via Telegram
func (c *Client) SendAlert(msg AlertMessage) error {
	c.mu.RLock()
	settings := c.settings
	c.mu.RUnlock()

	if !settings.Enabled || settings.BotToken == "" || settings.ChatID == "" {
		return nil
	}

	// Build the notification message
	var text string
	if msg.Status == "down" {
		text = fmt.Sprintf("🔴 *%s is DOWN*\n\n", escapeMarkdown(msg.Host))
	} else {
		text = fmt.Sprintf("✅ *%s is UP*\n\n", escapeMarkdown(msg.Host))
	}

	text += fmt.Sprintf("*Host:* %s \\(%s\\)\n", escapeMarkdown(msg.Host), escapeMarkdown(msg.Address))
	text += fmt.Sprintf("*Check:* %s", strings.ToUpper(msg.CheckType))
	if msg.CheckID != "" {
		text += fmt.Sprintf(" \\[%s\\]", escapeMarkdown(msg.CheckID))
	}
	text += "\n"

	if msg.Message != "" {
		text += fmt.Sprintf("*Details:* %s\n", escapeMarkdown(msg.Message))
	}
	if msg.Status == "up" && msg.LatencyMS > 0 {
		text += fmt.Sprintf("*Latency:* %dms\n", msg.LatencyMS)
	}

	// Send the request
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", settings.BotToken)
	data := url.Values{
		"chat_id":    {settings.ChatID},
		"text":       {text},
		"parse_mode": {"MarkdownV2"},
	}

	// Disable link preview if configured
	if settings.DisablePreview {
		data.Set("disable_web_page_preview", "true")
	}

	// Silent notification if configured
	if settings.Silent {
		data.Set("disable_notification", "true")
	}

	resp, err := c.http.PostForm(apiURL, data)
	if err != nil {
		return fmt.Errorf("telegram request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var result struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && result.Description != "" {
			return fmt.Errorf("telegram error: %s", result.Description)
		}
		return fmt.Errorf("telegram returned status %d", resp.StatusCode)
	}

	log.Printf("Telegram notification sent: %s - %s", msg.Host, msg.Status)
	return nil
}

// TestNotification sends a test notification
func (c *Client) TestNotification() error {
	c.mu.RLock()
	settings := c.settings
	c.mu.RUnlock()

	if settings.BotToken == "" || settings.ChatID == "" {
		return fmt.Errorf("telegram credentials not configured")
	}

	text := "✅ *POKE443 Test Notification*\n\nThis is a test notification from POKE443\\. If you see this, Telegram is configured correctly\\!"

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", settings.BotToken)
	data := url.Values{
		"chat_id":    {settings.ChatID},
		"text":       {text},
		"parse_mode": {"MarkdownV2"},
	}

	resp, err := c.http.PostForm(apiURL, data)
	if err != nil {
		return fmt.Errorf("telegram request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var result struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && result.Description != "" {
			return fmt.Errorf("telegram error: %s", result.Description)
		}
		return fmt.Errorf("telegram returned status %d", resp.StatusCode)
	}

	return nil
}

// escapeMarkdown escapes special characters for Telegram MarkdownV2
func escapeMarkdown(s string) string {
	// Characters that need to be escaped in MarkdownV2
	special := []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}
	result := s
	for _, char := range special {
		result = strings.ReplaceAll(result, char, "\\"+char)
	}
	return result
}
