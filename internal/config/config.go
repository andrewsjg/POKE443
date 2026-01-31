package config

import (
	"fmt"
	"path/filepath"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type CheckType string

const (
	CheckPing CheckType = "ping"
	CheckHTTP CheckType = "http"
	CheckTCP  CheckType = "tcp"
)

type Check struct {
	Type           CheckType `koanf:"type" json:"type" yaml:"type" toml:"type"`
	Enabled        bool      `koanf:"enabled" json:"enabled" yaml:"enabled" toml:"enabled"`
	URL            string    `koanf:"url" json:"url" yaml:"url" toml:"url"`
	Expect         int       `koanf:"expect" json:"expect" yaml:"expect" toml:"expect"`
	Port           int       `koanf:"port" json:"port" yaml:"port" toml:"port"`                                             // TCP port for tcp checks
	ID             string    `koanf:"id" json:"id" yaml:"id" toml:"id"`                                                     // Optional unique identifier for this check
	DependsOn      string    `koanf:"depends_on" json:"depends_on" yaml:"depends_on" toml:"depends_on"`                     // ID of check this depends on
	MQTTNotify     bool      `koanf:"mqtt_notify" json:"mqtt_notify" yaml:"mqtt_notify" toml:"mqtt_notify"`                 // Send MQTT notifications on state change
	PushoverNotify bool      `koanf:"pushover_notify" json:"pushover_notify" yaml:"pushover_notify" toml:"pushover_notify"` // Send Pushover notifications
	TelegramNotify bool      `koanf:"telegram_notify" json:"telegram_notify" yaml:"telegram_notify" toml:"telegram_notify"` // Send Telegram notifications
}

type Host struct {
	Name                string  `koanf:"name" json:"name" yaml:"name" toml:"name"`
	Address             string  `koanf:"address" json:"address" yaml:"address" toml:"address"`
	Checks              []Check `koanf:"checks" json:"checks" yaml:"checks" toml:"checks"`
	HealthchecksPingURL string  `koanf:"healthchecks_ping_url" json:"healthchecks_ping_url" yaml:"healthchecks_ping_url" toml:"healthchecks_ping_url"`
}

// MQTTSettings holds MQTT broker configuration
type MQTTSettings struct {
	Enabled  bool   `koanf:"enabled" json:"enabled" yaml:"enabled" toml:"enabled"`
	Broker   string `koanf:"broker" json:"broker" yaml:"broker" toml:"broker"` // e.g., tcp://localhost:1883
	Username string `koanf:"username" json:"username" yaml:"username" toml:"username"`
	Password string `koanf:"password" json:"password" yaml:"password" toml:"password"`
	Topic    string `koanf:"topic" json:"topic" yaml:"topic" toml:"topic"` // Base topic, e.g., healthchecker/status
	ClientID string `koanf:"client_id" json:"client_id" yaml:"client_id" toml:"client_id"`
}

// PushoverSettings holds Pushover notification configuration
type PushoverSettings struct {
	Enabled  bool   `koanf:"enabled" json:"enabled" yaml:"enabled" toml:"enabled"`
	APIToken string `koanf:"api_token" json:"api_token" yaml:"api_token" toml:"api_token"` // Application API token
	UserKey  string `koanf:"user_key" json:"user_key" yaml:"user_key" toml:"user_key"`     // User or group key
	Device   string `koanf:"device" json:"device" yaml:"device" toml:"device"`             // Optional: specific device name
	Sound    string `koanf:"sound" json:"sound" yaml:"sound" toml:"sound"`                 // Optional: notification sound
}

// TelegramSettings holds Telegram bot notification configuration
type TelegramSettings struct {
	Enabled        bool   `koanf:"enabled" json:"enabled" yaml:"enabled" toml:"enabled"`
	BotToken       string `koanf:"bot_token" json:"bot_token" yaml:"bot_token" toml:"bot_token"`                         // Bot token from @BotFather
	ChatID         string `koanf:"chat_id" json:"chat_id" yaml:"chat_id" toml:"chat_id"`                                 // Chat/group/channel ID
	DisablePreview bool   `koanf:"disable_preview" json:"disable_preview" yaml:"disable_preview" toml:"disable_preview"` // Disable link preview
	Silent         bool   `koanf:"silent" json:"silent" yaml:"silent" toml:"silent"`                                     // Send without notification sound
}

// Settings holds application-wide settings
type Settings struct {
	MQTT     MQTTSettings     `koanf:"mqtt" json:"mqtt" yaml:"mqtt" toml:"mqtt"`
	Pushover PushoverSettings `koanf:"pushover" json:"pushover" yaml:"pushover" toml:"pushover"`
	Telegram TelegramSettings `koanf:"telegram" json:"telegram" yaml:"telegram" toml:"telegram"`
}

type Config struct {
	Hosts    []Host   `koanf:"hosts" json:"hosts" yaml:"hosts" toml:"hosts"`
	Settings Settings `koanf:"settings" json:"settings" yaml:"settings" toml:"settings"`
}

func Load(path string) (*Config, error) {
	k := koanf.New("")
	ext := filepath.Ext(path)
	var parser koanf.Parser
	switch ext {
	case ".yaml", ".yml":
		parser = yaml.Parser()
	case ".toml":
		parser = toml.Parser()
	default:
		return nil, fmt.Errorf("unsupported config extension: %s", ext)
	}
	if err := k.Load(file.Provider(path), parser); err != nil {
		return nil, err
	}
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}
	// ensure at least one ping check if none provided
	for i := range cfg.Hosts {
		if len(cfg.Hosts[i].Checks) == 0 {
			cfg.Hosts[i].Checks = []Check{{Type: CheckPing, Enabled: true}}
		}
	}
	return &cfg, nil
}
