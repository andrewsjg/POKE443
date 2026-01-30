package state

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/andrewsjg/simple-healthchecker/copilot/internal/checks"
	"github.com/andrewsjg/simple-healthchecker/copilot/internal/config"
	"github.com/andrewsjg/simple-healthchecker/copilot/internal/mqtt"
)

// CheckDataPoint represents a single check result with timestamp
type CheckDataPoint struct {
	Timestamp time.Time
	OK        bool
	LatencyMS int64
}

// Event represents a state change (up->down or down->up)
type Event struct {
	Timestamp time.Time
	HostName  string
	CheckIdx  int
	CheckType config.CheckType
	EventType string // "down", "up", "recovered"
	Message   string
	Duration  time.Duration // For recovery events, how long it was down
}

type CheckStatus struct {
	Type           config.CheckType
	Enabled        bool
	OK             bool
	ParentFailed   bool   // True if this check's parent dependency is down
	ParentID       string // ID of the parent check this depends on
	Message        string
	LatencyMS      int64
	LatencyHistory []int64          // Rolling history for sparkline (last 20)
	FullHistory    []CheckDataPoint // Extended history for analytics (last 1000)
	CheckedAt      time.Time
	URL            string
	Expect         int
	Port           int    // TCP port for tcp checks
	ID             string // Unique identifier for this check (for dependencies)
	DependsOn      string // ID of the check this depends on
	MQTTNotify     bool   // Send MQTT notifications on state change
	// Uptime tracking
	TotalChecks   int64
	SuccessChecks int64
	LastDownAt    time.Time // When the check last went down
	LastUpAt      time.Time // When the check last came up
}

const (
	maxLatencyHistory = 20   // Keep last 20 data points for sparkline
	maxFullHistory    = 1000 // Keep last 1000 data points for analytics (~2.7 hours at 10s intervals)
)

// Global event log
var (
	eventLog      []Event
	eventLogMutex sync.RWMutex
	maxEvents     = 500
)

type HostStatus struct {
	Name    string
	Address string
	Checks  []CheckStatus
	HCURL   string
}

type State struct {
	mu         sync.RWMutex
	cfg        *config.Config
	hosts      map[string]*HostStatus  // key: host name
	checksByID map[string]*CheckStatus // lookup checks by ID for dependency resolution
	configPath string
	mqttClient *mqtt.Client
}

func New(cfg *config.Config) *State {
	// Initialize MQTT client
	mqttClient := mqtt.NewClient(cfg.Settings.MQTT)
	if cfg.Settings.MQTT.Enabled {
		if err := mqttClient.Connect(); err != nil {
			log.Printf("MQTT connection failed: %v", err)
		}
	}

	st := &State{
		cfg:        cfg,
		hosts:      make(map[string]*HostStatus),
		checksByID: make(map[string]*CheckStatus),
		mqttClient: mqttClient,
	}
	for _, h := range cfg.Hosts {
		hs := &HostStatus{Name: h.Name, Address: h.Address, HCURL: h.HealthchecksPingURL}
		for _, c := range h.Checks {
			cs := CheckStatus{
				Type:       c.Type,
				Enabled:    c.Enabled,
				ID:         c.ID,
				DependsOn:  c.DependsOn,
				MQTTNotify: c.MQTTNotify,
			}
			if c.Type == config.CheckHTTP {
				cs.URL = c.URL
				cs.Expect = c.Expect
			}
			if c.Type == config.CheckTCP {
				cs.Port = c.Port
			}
			hs.Checks = append(hs.Checks, cs)
		}
		st.hosts[h.Name] = hs
	}
	// Build check lookup map for dependency resolution
	st.rebuildCheckIndex()
	return st
}

// rebuildCheckIndex rebuilds the checksByID map after any changes
func (s *State) rebuildCheckIndex() {
	s.checksByID = make(map[string]*CheckStatus)
	for _, hs := range s.hosts {
		for i := range hs.Checks {
			if hs.Checks[i].ID != "" {
				s.checksByID[hs.Checks[i].ID] = &hs.Checks[i]
			}
		}
	}
}

// GetCheckByID returns a check by its ID
func (s *State) GetCheckByID(id string) (*CheckStatus, bool) {
	c, ok := s.checksByID[id]
	return c, ok
}

// IsParentOK checks if the parent dependency (if any) is OK
// Returns true if no dependency or parent is OK
func (s *State) IsParentOK(c *CheckStatus) bool {
	if c.DependsOn == "" {
		return true // No dependency
	}
	parent, ok := s.checksByID[c.DependsOn]
	if !ok {
		return true // Dependency not found, treat as OK
	}
	if !parent.Enabled {
		return true // Parent disabled, treat as OK
	}
	if parent.CheckedAt.IsZero() {
		return true // Parent not checked yet, treat as OK
	}
	// Recursively check parent's parent
	if !s.IsParentOK(parent) {
		return false
	}
	return parent.OK
}

// AggregateStats holds overall system health statistics
type AggregateStats struct {
	TotalHosts         int
	TotalChecks        int
	ChecksUp           int
	ChecksDown         int
	ChecksParentFailed int // Checks down due to parent failure
	ChecksDisabled     int
	ChecksUnknown      int
	OverallUptime      float64 // Percentage
}

// GetAggregateStats returns overall system health statistics
func (s *State) GetAggregateStats() AggregateStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := AggregateStats{TotalHosts: len(s.hosts)}

	var totalUptimeSum float64
	var uptimeCount int

	for _, hs := range s.hosts {
		for _, c := range hs.Checks {
			stats.TotalChecks++
			if !c.Enabled {
				stats.ChecksDisabled++
				continue
			}
			if c.CheckedAt.IsZero() {
				stats.ChecksUnknown++
				continue
			}
			if c.OK {
				stats.ChecksUp++
			} else if c.ParentFailed {
				stats.ChecksParentFailed++
			} else {
				stats.ChecksDown++
			}
			// Calculate uptime for this check
			if c.TotalChecks > 0 {
				totalUptimeSum += float64(c.SuccessChecks) / float64(c.TotalChecks) * 100
				uptimeCount++
			}
		}
	}

	if uptimeCount > 0 {
		stats.OverallUptime = totalUptimeSum / float64(uptimeCount)
	} else if stats.ChecksUp > 0 || stats.ChecksDown > 0 {
		// Fallback: calculate from current status if no historical data
		stats.OverallUptime = float64(stats.ChecksUp) / float64(stats.ChecksUp+stats.ChecksDown) * 100
	} else {
		// No data yet - default to 100%
		stats.OverallUptime = 100.0
	}

	return stats
}

// HostAnalytics contains detailed analytics for a single host
type HostAnalytics struct {
	Name             string
	Address          string
	Checks           []CheckAnalytics
	OverallUptime    float64
	HealthScore      int // 0-100
	HasBlockedChecks bool
}

// CheckAnalytics contains detailed analytics for a single check
type CheckAnalytics struct {
	Type          config.CheckType
	URL           string
	Enabled       bool
	OK            bool
	ParentFailed  bool
	LatencyMS     int64
	Uptime        float64 // Percentage
	AvgLatency    float64
	MinLatency    int64
	MaxLatency    int64
	P95Latency    int64
	TotalChecks   int64
	SuccessChecks int64
	FailedChecks  int64
	History       []CheckDataPoint
	HeatmapData   []bool // Last 60 check results for heatmap
}

// GetHostAnalytics returns detailed analytics for a specific host
func (s *State) GetHostAnalytics(hostName string) (HostAnalytics, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	hs, ok := s.hosts[hostName]
	if !ok {
		return HostAnalytics{}, false
	}

	analytics := HostAnalytics{
		Name:    hs.Name,
		Address: hs.Address,
	}

	var uptimeSum float64
	var healthSum int
	var hasBlockedChecks bool

	for _, c := range hs.Checks {
		ca := CheckAnalytics{
			Type:          c.Type,
			URL:           c.URL,
			Enabled:       c.Enabled,
			OK:            c.OK,
			ParentFailed:  c.ParentFailed,
			LatencyMS:     c.LatencyMS,
			TotalChecks:   c.TotalChecks,
			SuccessChecks: c.SuccessChecks,
			FailedChecks:  c.TotalChecks - c.SuccessChecks,
			History:       make([]CheckDataPoint, len(c.FullHistory)),
		}
		copy(ca.History, c.FullHistory)

		// Track if any checks are blocked by parent failure
		if c.ParentFailed {
			hasBlockedChecks = true
		}

		// Calculate uptime
		if c.TotalChecks > 0 {
			ca.Uptime = float64(c.SuccessChecks) / float64(c.TotalChecks) * 100
			uptimeSum += ca.Uptime
		}

		// Calculate latency stats from history
		if len(c.FullHistory) > 0 {
			var sum int64
			var count int64
			ca.MinLatency = c.FullHistory[0].LatencyMS
			ca.MaxLatency = c.FullHistory[0].LatencyMS
			latencies := make([]int64, 0, len(c.FullHistory))

			for _, dp := range c.FullHistory {
				if dp.OK && dp.LatencyMS > 0 {
					sum += dp.LatencyMS
					count++
					latencies = append(latencies, dp.LatencyMS)
					if dp.LatencyMS < ca.MinLatency {
						ca.MinLatency = dp.LatencyMS
					}
					if dp.LatencyMS > ca.MaxLatency {
						ca.MaxLatency = dp.LatencyMS
					}
				}
			}

			if count > 0 {
				ca.AvgLatency = float64(sum) / float64(count)
				// Calculate P95
				sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
				p95Idx := int(float64(len(latencies)) * 0.95)
				if p95Idx >= len(latencies) {
					p95Idx = len(latencies) - 1
				}
				ca.P95Latency = latencies[p95Idx]
			}
		}

		// Build heatmap data (last 60 results)
		heatmapSize := 60
		startIdx := len(c.FullHistory) - heatmapSize
		if startIdx < 0 {
			startIdx = 0
		}
		ca.HeatmapData = make([]bool, 0, heatmapSize)
		for i := startIdx; i < len(c.FullHistory); i++ {
			ca.HeatmapData = append(ca.HeatmapData, c.FullHistory[i].OK)
		}

		// Calculate health score contribution (0-100)
		checkHealth := 0
		if c.Enabled && c.TotalChecks > 0 {
			checkHealth = int(ca.Uptime)
		} else if c.Enabled {
			checkHealth = 50 // Unknown
		}
		healthSum += checkHealth

		analytics.Checks = append(analytics.Checks, ca)
	}

	if len(hs.Checks) > 0 {
		analytics.OverallUptime = uptimeSum / float64(len(hs.Checks))
		analytics.HealthScore = healthSum / len(hs.Checks)
	}
	analytics.HasBlockedChecks = hasBlockedChecks

	return analytics, true
}

// GetAllHostAnalytics returns analytics for all hosts
func (s *State) GetAllHostAnalytics() []HostAnalytics {
	s.mu.RLock()
	hostNames := make([]string, 0, len(s.hosts))
	for name := range s.hosts {
		hostNames = append(hostNames, name)
	}
	s.mu.RUnlock()

	result := make([]HostAnalytics, 0, len(hostNames))
	for _, name := range hostNames {
		if analytics, ok := s.GetHostAnalytics(name); ok {
			result = append(result, analytics)
		}
	}

	// Sort by config order
	s.mu.RLock()
	order := make(map[string]int)
	for i, h := range s.cfg.Hosts {
		order[h.Name] = i
	}
	s.mu.RUnlock()

	sort.Slice(result, func(i, j int) bool {
		return order[result[i].Name] < order[result[j].Name]
	})

	return result
}

func (s *State) Snapshot() []*HostStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// stable order: by cfg order
	order := make(map[string]int, len(s.cfg.Hosts))
	for i, h := range s.cfg.Hosts {
		order[h.Name] = i
	}
	out := make([]*HostStatus, 0, len(s.hosts))
	for _, v := range s.hosts {
		copy := *v
		copy.Checks = append([]CheckStatus(nil), v.Checks...)
		out = append(out, &copy)
	}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].Name] < order[out[j].Name] })
	return out
}

func (s *State) AddHost(name, address, hcurl string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.hosts[name]; exists {
		return fmt.Errorf("host exists")
	}
	hs := &HostStatus{Name: name, Address: address, HCURL: hcurl}
	hs.Checks = append(hs.Checks, CheckStatus{Type: config.CheckPing, Enabled: true})
	s.hosts[name] = hs
	// update cfg
	s.cfg.Hosts = append(s.cfg.Hosts, config.Host{
		Name: name, Address: address, HealthchecksPingURL: hcurl,
		Checks: []config.Check{{Type: config.CheckPing, Enabled: true}},
	})
	return s.saveConfigLocked()
}

func (s *State) AddHostWithoutDefaultCheck(name, address, hcurl string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.hosts[name]; exists {
		return fmt.Errorf("host exists")
	}
	hs := &HostStatus{Name: name, Address: address, HCURL: hcurl}
	s.hosts[name] = hs
	// update cfg
	s.cfg.Hosts = append(s.cfg.Hosts, config.Host{
		Name: name, Address: address, HealthchecksPingURL: hcurl,
	})
	return s.saveConfigLocked()
}

func (s *State) GetHost(name string) (HostStatus, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if hs, ok := s.hosts[name]; ok {
		copy := *hs
		copy.Checks = append([]CheckStatus(nil), hs.Checks...)
		return copy, true
	}
	return HostStatus{}, false
}

func (s *State) UpdateHost(oldName, newName, address, hcurl string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, ok := s.hosts[oldName]
	if !ok {
		return fmt.Errorf("host not found")
	}
	if newName != oldName {
		if _, exists := s.hosts[newName]; exists {
			return fmt.Errorf("host name already exists")
		}
		delete(s.hosts, oldName)
		hs.Name = newName
		s.hosts[newName] = hs
	} else {
		hs.Name = newName
	}
	hs.Address = address
	hs.HCURL = hcurl
	// update cfg
	for i := range s.cfg.Hosts {
		if s.cfg.Hosts[i].Name == oldName {
			s.cfg.Hosts[i].Name = newName
			s.cfg.Hosts[i].Address = address
			s.cfg.Hosts[i].HealthchecksPingURL = hcurl
			break
		}
	}
	return s.saveConfigLocked()
}

func (s *State) AddHTTPCheck(hostName, url string, expect int, id, dependsOn string, mqttNotify bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, ok := s.hosts[hostName]
	if !ok {
		return fmt.Errorf("host not found")
	}
	// append to runtime
	hs.Checks = append(hs.Checks, CheckStatus{Type: config.CheckHTTP, Enabled: true, URL: url, Expect: expect, ID: id, DependsOn: dependsOn, MQTTNotify: mqttNotify})
	// append to cfg
	for i := range s.cfg.Hosts {
		if s.cfg.Hosts[i].Name == hostName {
			s.cfg.Hosts[i].Checks = append(s.cfg.Hosts[i].Checks, config.Check{Type: config.CheckHTTP, Enabled: true, URL: url, Expect: expect, ID: id, DependsOn: dependsOn, MQTTNotify: mqttNotify})
			break
		}
	}
	s.rebuildCheckIndex()
	return s.saveConfigLocked()
}

func (s *State) DeleteHost(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hosts[name]; !ok {
		return fmt.Errorf("host not found")
	}
	delete(s.hosts, name)
	// remove from cfg
	for i := range s.cfg.Hosts {
		if s.cfg.Hosts[i].Name == name {
			s.cfg.Hosts = append(s.cfg.Hosts[:i], s.cfg.Hosts[i+1:]...)
			break
		}
	}
	return s.saveConfigLocked()
}

func (s *State) AddPingCheck(hostName, id, dependsOn string, mqttNotify bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, ok := s.hosts[hostName]
	if !ok {
		return fmt.Errorf("host not found")
	}
	hs.Checks = append(hs.Checks, CheckStatus{Type: config.CheckPing, Enabled: true, ID: id, DependsOn: dependsOn, MQTTNotify: mqttNotify})
	for i := range s.cfg.Hosts {
		if s.cfg.Hosts[i].Name == hostName {
			s.cfg.Hosts[i].Checks = append(s.cfg.Hosts[i].Checks, config.Check{Type: config.CheckPing, Enabled: true, ID: id, DependsOn: dependsOn, MQTTNotify: mqttNotify})
			break
		}
	}
	s.rebuildCheckIndex()
	return s.saveConfigLocked()
}

func (s *State) AddTCPCheck(hostName string, port int, id, dependsOn string, mqttNotify bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, ok := s.hosts[hostName]
	if !ok {
		return fmt.Errorf("host not found")
	}
	hs.Checks = append(hs.Checks, CheckStatus{Type: config.CheckTCP, Enabled: true, Port: port, ID: id, DependsOn: dependsOn, MQTTNotify: mqttNotify})
	for i := range s.cfg.Hosts {
		if s.cfg.Hosts[i].Name == hostName {
			s.cfg.Hosts[i].Checks = append(s.cfg.Hosts[i].Checks, config.Check{Type: config.CheckTCP, Enabled: true, Port: port, ID: id, DependsOn: dependsOn, MQTTNotify: mqttNotify})
			break
		}
	}
	s.rebuildCheckIndex()
	return s.saveConfigLocked()
}

func (s *State) RemoveCheck(hostName string, idx int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, ok := s.hosts[hostName]
	if !ok {
		return fmt.Errorf("host not found")
	}
	if idx < 0 || idx >= len(hs.Checks) {
		return fmt.Errorf("bad index")
	}
	hs.Checks = append(hs.Checks[:idx], hs.Checks[idx+1:]...)
	for i := range s.cfg.Hosts {
		if s.cfg.Hosts[i].Name == hostName {
			if idx < 0 || idx >= len(s.cfg.Hosts[i].Checks) {
				break
			}
			s.cfg.Hosts[i].Checks = append(s.cfg.Hosts[i].Checks[:idx], s.cfg.Hosts[i].Checks[idx+1:]...)
			break
		}
	}
	return s.saveConfigLocked()
}

func (s *State) UpdateHTTPCheck(hostName string, idx int, url string, expect int, id, dependsOn string, mqttNotify bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, ok := s.hosts[hostName]
	if !ok {
		return fmt.Errorf("host not found")
	}
	if idx < 0 || idx >= len(hs.Checks) {
		return fmt.Errorf("bad index")
	}
	if hs.Checks[idx].Type != config.CheckHTTP {
		return fmt.Errorf("not http check")
	}
	hs.Checks[idx].URL = url
	hs.Checks[idx].Expect = expect
	hs.Checks[idx].ID = id
	hs.Checks[idx].DependsOn = dependsOn
	hs.Checks[idx].MQTTNotify = mqttNotify
	for i := range s.cfg.Hosts {
		if s.cfg.Hosts[i].Name == hostName {
			if idx < 0 || idx >= len(s.cfg.Hosts[i].Checks) {
				break
			}
			s.cfg.Hosts[i].Checks[idx].URL = url
			s.cfg.Hosts[i].Checks[idx].Expect = expect
			s.cfg.Hosts[i].Checks[idx].ID = id
			s.cfg.Hosts[i].Checks[idx].DependsOn = dependsOn
			s.cfg.Hosts[i].Checks[idx].MQTTNotify = mqttNotify
			break
		}
	}
	s.rebuildCheckIndex()
	return s.saveConfigLocked()
}

func (s *State) UpdateTCPCheck(hostName string, idx int, port int, id, dependsOn string, mqttNotify bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, ok := s.hosts[hostName]
	if !ok {
		return fmt.Errorf("host not found")
	}
	if idx < 0 || idx >= len(hs.Checks) {
		return fmt.Errorf("bad index")
	}
	if hs.Checks[idx].Type != config.CheckTCP {
		return fmt.Errorf("not tcp check")
	}
	hs.Checks[idx].Port = port
	hs.Checks[idx].ID = id
	hs.Checks[idx].DependsOn = dependsOn
	hs.Checks[idx].MQTTNotify = mqttNotify
	for i := range s.cfg.Hosts {
		if s.cfg.Hosts[i].Name == hostName {
			if idx < 0 || idx >= len(s.cfg.Hosts[i].Checks) {
				break
			}
			s.cfg.Hosts[i].Checks[idx].Port = port
			s.cfg.Hosts[i].Checks[idx].ID = id
			s.cfg.Hosts[i].Checks[idx].DependsOn = dependsOn
			s.cfg.Hosts[i].Checks[idx].MQTTNotify = mqttNotify
			break
		}
	}
	s.rebuildCheckIndex()
	return s.saveConfigLocked()
}

// UpdateCheckDependencies updates the ID, DependsOn, and MQTTNotify fields for a check
func (s *State) UpdateCheckDependencies(hostName string, idx int, id, dependsOn string, mqttNotify bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, ok := s.hosts[hostName]
	if !ok {
		return fmt.Errorf("host not found")
	}
	if idx < 0 || idx >= len(hs.Checks) {
		return fmt.Errorf("bad index")
	}
	hs.Checks[idx].ID = id
	hs.Checks[idx].DependsOn = dependsOn
	hs.Checks[idx].MQTTNotify = mqttNotify
	for i := range s.cfg.Hosts {
		if s.cfg.Hosts[i].Name == hostName {
			if idx < 0 || idx >= len(s.cfg.Hosts[i].Checks) {
				break
			}
			s.cfg.Hosts[i].Checks[idx].ID = id
			s.cfg.Hosts[i].Checks[idx].DependsOn = dependsOn
			s.cfg.Hosts[i].Checks[idx].MQTTNotify = mqttNotify
			break
		}
	}
	s.rebuildCheckIndex()
	return s.saveConfigLocked()
}

func (s *State) Toggle(hostName string, idx int, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if hs, ok := s.hosts[hostName]; ok {
		if idx >= 0 && idx < len(hs.Checks) {
			hs.Checks[idx].Enabled = enabled
		}
	}
}

func (s *State) SetAllEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, hs := range s.hosts {
		for i := range hs.Checks {
			hs.Checks[i].Enabled = enabled
		}
	}
}

func (s *State) SetConfigPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if abs, err := filepath.Abs(path); err == nil {
		s.configPath = abs
	} else {
		s.configPath = path
	}
}

func (s *State) SetHCURL(hostName, url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if hs, ok := s.hosts[hostName]; ok {
		hs.HCURL = url
		// also persist into cfg
		found := false
		for i := range s.cfg.Hosts {
			if s.cfg.Hosts[i].Name == hostName {
				s.cfg.Hosts[i].HealthchecksPingURL = url
				found = true
				break
			}
		}
		if !found {
			log.Printf("warning: host %q not found in cfg when saving HCURL", hostName)
		}
		if err := s.saveConfigLocked(); err != nil {
			log.Printf("persist config failed: %v", err)
		} else {
			log.Printf("persist config ok: %s", s.configPath)
		}
	} else {
		log.Printf("warning: host %q not found in state when setting HCURL", hostName)
	}
}

func (s *State) StartScheduler(interval time.Duration, stop <-chan struct{}) {
	go func() {
		// run immediately, then on each tick
		s.runOnce()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				fmt.Println("scheduler tick")
				s.runOnce()
			case <-stop:
				return
			}
		}
	}()
}

func (s *State) runOnce() {
	fmt.Println("running checks")
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, hs := range s.hosts {
		for i := range hs.Checks {
			c := &hs.Checks[i]
			if !c.Enabled {
				continue
			}

			wasOK := c.OK
			wasChecked := !c.CheckedAt.IsZero()
			wasParentFailed := c.ParentFailed

			// Check if parent dependency is failing
			parentOK := s.IsParentOK(c)
			if c.DependsOn != "" {
				c.ParentID = c.DependsOn
			}

			switch c.Type {
			case config.CheckPing:
				res := checks.PingOnce(hs.Address, 2*time.Second)
				c.CheckedAt = now
				actualOK := res.OK

				if res.OK {
					c.OK = true
					c.ParentFailed = false
					c.Message = "pong"
					c.LatencyMS = res.Latency.Milliseconds()
					if hs.HCURL != "" {
						_ = notifyHealthchecksOK(hs.HCURL)
					}
				} else {
					// Check failed - is it because parent is down?
					if !parentOK {
						c.OK = false
						c.ParentFailed = true
						c.Message = "parent check failed"
						c.LatencyMS = 0
						// Don't notify healthchecks when parent is down
					} else {
						c.OK = false
						c.ParentFailed = false
						if res.Err != nil {
							c.Message = res.Err.Error()
						} else {
							c.Message = "no reply"
						}
						c.LatencyMS = 0
						if hs.HCURL != "" {
							_ = notifyHealthchecksFail(hs.HCURL)
						}
					}
				}
				// Record actual result for analytics
				c.recordDataPoint(now, actualOK, c.LatencyMS)

			case config.CheckHTTP:
				url := c.URL
				if url == "" {
					url = "http://" + hs.Address
				}
				res := checks.HTTPGet(url, 5*time.Second)
				c.CheckedAt = now

				actualOK := false
				if res.Err == nil {
					expect := c.Expect
					if expect == 0 {
						expect = 200
					}
					actualOK = (res.Code == expect)
				}

				if actualOK {
					c.OK = true
					c.ParentFailed = false
					c.Message = fmt.Sprintf("status %d (expect %d)", res.Code, c.Expect)
					if c.Expect == 0 {
						c.Message = fmt.Sprintf("status %d (expect %d)", res.Code, 200)
					}
					c.LatencyMS = res.Latency.Milliseconds()
				} else {
					// Check failed - is it because parent is down?
					if !parentOK {
						c.OK = false
						c.ParentFailed = true
						c.Message = "parent check failed"
						c.LatencyMS = 0
					} else {
						c.OK = false
						c.ParentFailed = false
						if res.Err != nil {
							c.Message = res.Err.Error()
						} else {
							expect := c.Expect
							if expect == 0 {
								expect = 200
							}
							c.Message = fmt.Sprintf("status %d (expect %d)", res.Code, expect)
						}
						c.LatencyMS = res.Latency.Milliseconds()
					}
				}
				// Record actual result for analytics
				c.recordDataPoint(now, actualOK, c.LatencyMS)

			case config.CheckTCP:
				port := c.Port
				if port == 0 {
					port = 80 // default port
				}
				res := checks.TCPCheck(hs.Address, port, 5*time.Second)
				c.CheckedAt = now
				actualOK := res.OK

				if res.OK {
					c.OK = true
					c.ParentFailed = false
					c.Message = fmt.Sprintf("port %d open", port)
					c.LatencyMS = res.Latency.Milliseconds()
				} else {
					// Check failed - is it because parent is down?
					if !parentOK {
						c.OK = false
						c.ParentFailed = true
						c.Message = "parent check failed"
						c.LatencyMS = 0
					} else {
						c.OK = false
						c.ParentFailed = false
						if res.Err != nil {
							c.Message = res.Err.Error()
						} else {
							c.Message = fmt.Sprintf("port %d closed", port)
						}
						c.LatencyMS = 0
					}
				}
				// Record actual result for analytics
				c.recordDataPoint(now, actualOK, c.LatencyMS)
			}

			// Track state changes for events (only fire events when not parent-failed)
			if wasChecked {
				if wasOK && !c.OK && !c.ParentFailed {
					// Went down (genuine failure, not parent-related)
					c.LastDownAt = now
					logEvent(Event{
						Timestamp: now,
						HostName:  hs.Name,
						CheckIdx:  i,
						CheckType: c.Type,
						EventType: "down",
						Message:   c.Message,
					})
					// MQTT notification
					if c.MQTTNotify && s.mqttClient != nil {
						s.publishMQTTStateChange(hs.Name, hs.Address, c, "down")
					}
				} else if !wasOK && c.OK {
					// Recovered
					duration := time.Duration(0)
					if !c.LastDownAt.IsZero() {
						duration = now.Sub(c.LastDownAt)
					}
					c.LastUpAt = now
					// Only log recovery event if we weren't previously parent-failed
					if !wasParentFailed {
						logEvent(Event{
							Timestamp: now,
							HostName:  hs.Name,
							CheckIdx:  i,
							CheckType: c.Type,
							EventType: "recovered",
							Message:   fmt.Sprintf("Back up after %v", duration.Round(time.Second)),
							Duration:  duration,
						})
						// MQTT notification
						if c.MQTTNotify && s.mqttClient != nil {
							s.publishMQTTStateChange(hs.Name, hs.Address, c, "up")
						}
					}
				} else if wasParentFailed && !c.ParentFailed && !c.OK {
					// Parent recovered but we're still down - now fire the actual down event
					c.LastDownAt = now
					logEvent(Event{
						Timestamp: now,
						HostName:  hs.Name,
						CheckIdx:  i,
						CheckType: c.Type,
						EventType: "down",
						Message:   c.Message,
					})
					// MQTT notification
					if c.MQTTNotify && s.mqttClient != nil {
						s.publishMQTTStateChange(hs.Name, hs.Address, c, "down")
					}
				}
			}
		}
	}
}

// recordDataPoint adds a data point and updates uptime stats
func (c *CheckStatus) recordDataPoint(ts time.Time, ok bool, latencyMS int64) {
	// Update sparkline history
	c.LatencyHistory = append(c.LatencyHistory, latencyMS)
	if len(c.LatencyHistory) > maxLatencyHistory {
		c.LatencyHistory = c.LatencyHistory[1:]
	}

	// Update full history for analytics
	c.FullHistory = append(c.FullHistory, CheckDataPoint{
		Timestamp: ts,
		OK:        ok,
		LatencyMS: latencyMS,
	})
	if len(c.FullHistory) > maxFullHistory {
		c.FullHistory = c.FullHistory[1:]
	}

	// Update uptime counters
	c.TotalChecks++
	if ok {
		c.SuccessChecks++
	}
}

// logEvent adds an event to the global event log
func logEvent(e Event) {
	eventLogMutex.Lock()
	defer eventLogMutex.Unlock()
	eventLog = append(eventLog, e)
	if len(eventLog) > maxEvents {
		eventLog = eventLog[1:]
	}
	log.Printf("EVENT: %s - %s check on %s: %s", e.EventType, e.CheckType, e.HostName, e.Message)
}

// GetEvents returns recent events, optionally filtered
func GetEvents(limit int) []Event {
	eventLogMutex.RLock()
	defer eventLogMutex.RUnlock()
	if limit <= 0 || limit > len(eventLog) {
		limit = len(eventLog)
	}
	// Return most recent first
	result := make([]Event, limit)
	for i := 0; i < limit; i++ {
		result[i] = eventLog[len(eventLog)-1-i]
	}
	return result
}

func (s *State) saveConfigLocked() error {
	if s.configPath == "" {
		return nil
	}
	// sync HC URLs and MQTTNotify from runtime state to cfg before writing
	for i := range s.cfg.Hosts {
		name := s.cfg.Hosts[i].Name
		if hs, ok := s.hosts[name]; ok {
			s.cfg.Hosts[i].HealthchecksPingURL = hs.HCURL
			// Sync MQTTNotify for each check
			for j := range s.cfg.Hosts[i].Checks {
				if j < len(hs.Checks) {
					s.cfg.Hosts[i].Checks[j].MQTTNotify = hs.Checks[j].MQTTNotify
				}
			}
		}
	}
	ext := filepath.Ext(s.configPath)
	switch ext {
	case ".yaml", ".yml":
		b, err := yaml.Marshal(s.cfg)
		if err != nil {
			return err
		}
		tmp := s.configPath + ".tmp"
		if err := os.WriteFile(tmp, b, 0644); err != nil {
			return err
		}
		if err := os.Rename(tmp, s.configPath); err != nil {
			// fallback: write directly
			if werr := os.WriteFile(s.configPath, b, 0644); werr != nil {
				return werr
			}
		}
		log.Printf("saved config to %s", s.configPath)
		return nil
	case ".toml":
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(s.cfg); err != nil {
			return err
		}
		tmp := s.configPath + ".tmp"
		if err := os.WriteFile(tmp, buf.Bytes(), 0644); err != nil {
			return err
		}
		if err := os.Rename(tmp, s.configPath); err != nil {
			if werr := os.WriteFile(s.configPath, buf.Bytes(), 0644); werr != nil {
				return werr
			}
		}
		log.Printf("saved config to %s", s.configPath)
		return nil
	default:
		return nil
	}
}

func notifyHealthchecksFail(base string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	url := base
	if url != "" && url[len(url)-1] != '/' {
		url += "/fail"
	} else {
		url += "fail"
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func notifyHealthchecksOK(base string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, base, nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// publishMQTTStateChange publishes a state change to MQTT
func (s *State) publishMQTTStateChange(hostName, address string, c *CheckStatus, status string) {
	if s.mqttClient == nil {
		return
	}
	msg := mqtt.StateChangeMessage{
		Timestamp: time.Now(),
		Host:      hostName,
		Address:   address,
		CheckType: string(c.Type),
		CheckID:   c.ID,
		Status:    status,
		LatencyMS: c.LatencyMS,
		Message:   c.Message,
	}
	if c.Type == config.CheckHTTP {
		msg.CheckURL = c.URL
	}
	if err := s.mqttClient.PublishStateChange(msg); err != nil {
		log.Printf("MQTT publish error: %v", err)
	}
}

// GetMQTTSettings returns the current MQTT settings
func (s *State) GetMQTTSettings() config.MQTTSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Settings.MQTT
}

// UpdateMQTTSettings updates the MQTT settings
func (s *State) UpdateMQTTSettings(settings config.MQTTSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cfg.Settings.MQTT = settings
	if err := s.mqttClient.UpdateSettings(settings); err != nil {
		return err
	}
	return s.saveConfigLocked()
}

// IsMQTTConnected returns whether the MQTT client is connected
func (s *State) IsMQTTConnected() bool {
	if s.mqttClient == nil {
		return false
	}
	return s.mqttClient.IsConnected()
}
