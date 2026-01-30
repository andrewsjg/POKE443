package server

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"

	"github.com/andrewsjg/simple-healthchecker/copilot/internal/config"
	"github.com/andrewsjg/simple-healthchecker/copilot/internal/state"
)

//go:embed templates/*
var templatesFS embed.FS

type Server struct {
	st   *state.State
	http *http.Server
	tpl  *template.Template
}

func New(st *state.State) *Server {
	funcs := template.FuncMap{
		"slug": func(s string) string {
			b := make([]rune, 0, len(s))
			for _, r := range s {
				if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
					b = append(b, r)
				} else {
					b = append(b, '-')
				}
			}
			return string(b)
		},
		"sparkline":              generateSparklineSVG,
		"donutChart":             generateDonutChartSVG,
		"heatmap":                generateHeatmapSVG,
		"uptimeBar":              generateUptimeBarSVG,
		"smokepingChart":         generateSmokepingChartSVG,
		"formatUptime":           formatUptime,
		"healthColor":            healthScoreColor,
		"healthColorWithBlocked": healthScoreColorWithBlocked,
		"checkUptime":            calculateCheckUptime,
		"checkHeatmap":           extractHeatmapData,
	}
	tpl := template.Must(template.New("").Funcs(funcs).ParseFS(templatesFS, "templates/*.html", "templates/check_config_fragment.html"))
	return &Server{st: st, tpl: tpl}
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/toggle", s.handleToggle)
	mux.HandleFunc("/hcurl", s.handleHCURL)
	mux.HandleFunc("/addhost", s.handleAddHost)
	mux.HandleFunc("/addhost-form", s.handleAddHostForm)
	mux.HandleFunc("/close-modal", s.handleCloseModal)
	mux.HandleFunc("/addhost-check-row", s.handleAddHostCheckRow)
	mux.HandleFunc("/hosts", s.handleHosts)
	mux.HandleFunc("/edithost-form", s.handleEditHostForm)
	mux.HandleFunc("/edithost", s.handleEditHost)
	mux.HandleFunc("/delhost", s.handleDeleteHost)
	mux.HandleFunc("/edithost-addcheck", s.handleEditAddCheck)
	mux.HandleFunc("/edithost-delcheck", s.handleEditDelCheck)
	mux.HandleFunc("/edithost-updatecheck", s.handleEditUpdateCheck)
	mux.HandleFunc("/edithost-savechecks", s.handleEditSaveChecks)
	mux.HandleFunc("/check-config", s.handleCheckConfig)
	mux.HandleFunc("/silence-all", s.handleSilenceAll)
	mux.HandleFunc("/enable-all", s.handleEnableAll)
	mux.HandleFunc("/stats", s.handleStats)
	mux.HandleFunc("/analytics", s.handleAnalytics)
	mux.HandleFunc("/analytics/host", s.handleHostAnalytics)
	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/settings", s.handleSettings)
	mux.HandleFunc("/settings/mqtt", s.handleSettingsMQTT)
	s.http = &http.Server{Addr: addr, Handler: logRequests(mux)}
	return s.http.ListenAndServe()
}

func (s *Server) Stop() error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(context.Background())
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Hosts []*state.HostStatus
		Stats state.AggregateStats
	}{
		Hosts: s.st.Snapshot(),
		Stats: s.st.GetAggregateStats(),
	}
	_ = s.tpl.ExecuteTemplate(w, "index.html", data)
}

func (s *Server) handleToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	host := r.FormValue("host")
	idxStr := r.FormValue("idx")
	enabled := r.FormValue("enabled") == "true"
	idx, _ := strconv.Atoi(idxStr)
	s.st.Toggle(host, idx, enabled)
	_, _ = fmt.Fprint(w, toggleButton(host, idx, enabled))
}

func (s *Server) handleAddHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	name := r.FormValue("name")
	addr := r.FormValue("address")
	hcurl := r.FormValue("hcurl")
	if name == "" || addr == "" {
		w.WriteHeader(400)
		_, _ = w.Write([]byte("name and address required"))
		return
	}
	if err := s.st.AddHostWithoutDefaultCheck(name, addr, hcurl); err != nil {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(err.Error()))
		return
	}

	// Process checks from the form (checks_type, checks_url, etc. are arrays)
	_ = r.ParseForm()
	types := r.Form["checks_type"]
	urls := r.Form["checks_url"]
	expects := r.Form["checks_expect"]
	ports := r.Form["checks_port"]
	ids := r.Form["checks_id"]
	dependsOns := r.Form["checks_depends_on"]
	mqttNotifies := r.Form["checks_mqtt_notify"]

	// Also check the direct checkbox (not in hidden fields)
	directMqttNotify := r.FormValue("mqtt_notify") == "true"
	directType := r.FormValue("type")
	directURL := r.FormValue("url")
	directPort := r.FormValue("port")
	directID := r.FormValue("id")
	directDependsOn := r.FormValue("depends_on")
	directExpectStr := r.FormValue("expect")

	// If no checks were added via "Add" button, use the current form state
	if len(types) == 0 {
		// Add check based on current form state (type selector, mqtt checkbox, etc.)
		switch directType {
		case "http":
			expectVal := 200
			if directExpectStr != "" {
				if v, err := strconv.Atoi(directExpectStr); err == nil {
					expectVal = v
				}
			}
			_ = s.st.AddHTTPCheck(name, directURL, expectVal, directID, directDependsOn, directMqttNotify)
		case "tcp":
			portVal := 0
			if directPort != "" {
				if v, err := strconv.Atoi(directPort); err == nil {
					portVal = v
				}
			}
			_ = s.st.AddTCPCheck(name, portVal, directID, directDependsOn, directMqttNotify)
		default:
			// Default to ping
			_ = s.st.AddPingCheck(name, directID, directDependsOn, directMqttNotify)
		}
	} else {
		for i, typ := range types {
			id := ""
			if i < len(ids) {
				id = ids[i]
			}
			dependsOn := ""
			if i < len(dependsOns) {
				dependsOn = dependsOns[i]
			}
			mqttNotify := false
			if i < len(mqttNotifies) {
				mqttNotify = mqttNotifies[i] == "true"
			}

			switch typ {
			case "ping":
				_ = s.st.AddPingCheck(name, id, dependsOn, mqttNotify)
			case "http":
				url := ""
				if i < len(urls) {
					url = urls[i]
				}
				expect := 200
				if i < len(expects) {
					if v, err := strconv.Atoi(expects[i]); err == nil {
						expect = v
					}
				}
				_ = s.st.AddHTTPCheck(name, url, expect, id, dependsOn, mqttNotify)
			case "tcp":
				port := 0
				if i < len(ports) {
					if v, err := strconv.Atoi(ports[i]); err == nil {
						port = v
					}
				}
				_ = s.st.AddTCPCheck(name, port, id, dependsOn, mqttNotify)
			}
		}
	}

	// Return refreshed hosts grid
	data := struct{ Hosts []*state.HostStatus }{Hosts: s.st.Snapshot()}
	_ = s.tpl.ExecuteTemplate(w, "add_host_result.html", data)
}

func (s *Server) handleAddHostForm(w http.ResponseWriter, r *http.Request) {
	_ = s.tpl.ExecuteTemplate(w, "addhost_modal.html", nil)
}

func (s *Server) handleCloseModal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(""))
}

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	data := struct{ Hosts []*state.HostStatus }{Hosts: s.st.Snapshot()}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tpl.ExecuteTemplate(w, "hosts.html", data)
}

func (s *Server) handleAddHostCheckRow(w http.ResponseWriter, r *http.Request) {
	typ := r.FormValue("type")
	url := r.FormValue("url")
	expectStr := r.FormValue("expect")
	portStr := r.FormValue("port")
	id := r.FormValue("id")
	dependsOn := r.FormValue("depends_on")
	mqttNotify := r.FormValue("mqtt_notify") == "true"
	expect := 200
	if expectStr != "" {
		if v, err := strconv.Atoi(expectStr); err == nil {
			expect = v
		}
	}
	port := 0
	if portStr != "" {
		if v, err := strconv.Atoi(portStr); err == nil {
			port = v
		}
	}
	data := map[string]any{"Type": typ, "URL": url, "Expect": expect, "Port": port, "ID": id, "DependsOn": dependsOn, "MQTTNotify": mqttNotify}
	_ = s.tpl.ExecuteTemplate(w, "addhost_check_row.html", data)
}

func (s *Server) handleEditHostForm(w http.ResponseWriter, r *http.Request) {
	host := r.FormValue("host")
	hs, ok := s.st.GetHost(host)
	if !ok {
		w.WriteHeader(404)
		return
	}
	_ = s.tpl.ExecuteTemplate(w, "edithost_modal.html", hs)
}

func (s *Server) handleEditHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	old := r.FormValue("old_name")
	name := r.FormValue("name")
	addr := r.FormValue("address")
	hcurl := r.FormValue("hcurl")
	if name == "" || addr == "" {
		w.WriteHeader(400)
		return
	}
	if err := s.st.UpdateHost(old, name, addr, hcurl); err != nil {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(err.Error()))
		return
	}

	// Also save check changes
	hostForChecks := name // Use the new name after rename
	countStr := r.FormValue("check_count")
	count, _ := strconv.Atoi(countStr)

	for i := 0; i < count; i++ {
		typ := r.FormValue(fmt.Sprintf("type_%d", i))
		id := r.FormValue(fmt.Sprintf("id_%d", i))
		dependsOn := r.FormValue(fmt.Sprintf("depends_on_%d", i))
		mqttNotify := r.FormValue(fmt.Sprintf("mqtt_notify_%d", i)) == "true"

		if typ == "http" {
			url := r.FormValue(fmt.Sprintf("url_%d", i))
			expectStr := r.FormValue(fmt.Sprintf("expect_%d", i))
			expect := 200
			if expectStr != "" {
				if v, err := strconv.Atoi(expectStr); err == nil {
					expect = v
				}
			}
			_ = s.st.UpdateHTTPCheck(hostForChecks, i, url, expect, id, dependsOn, mqttNotify)
		} else if typ == "tcp" {
			portStr := r.FormValue(fmt.Sprintf("port_%d", i))
			port := 0
			if portStr != "" {
				if v, err := strconv.Atoi(portStr); err == nil {
					port = v
				}
			}
			_ = s.st.UpdateTCPCheck(hostForChecks, i, port, id, dependsOn, mqttNotify)
		} else if typ == "ping" {
			// For ping checks, just update the dependencies
			_ = s.st.UpdateCheckDependencies(hostForChecks, i, id, dependsOn, mqttNotify)
		}
	}

	data := struct{ Hosts []*state.HostStatus }{Hosts: s.st.Snapshot()}
	_ = s.tpl.ExecuteTemplate(w, "add_host_result.html", data)
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	name := r.FormValue("name")
	if err := s.st.DeleteHost(name); err != nil {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	data := struct{ Hosts []*state.HostStatus }{Hosts: s.st.Snapshot()}
	_ = s.tpl.ExecuteTemplate(w, "add_host_result.html", data)
}

func (s *Server) handleCheckConfig(w http.ResponseWriter, r *http.Request) {
	typ := r.FormValue("type")
	_ = s.tpl.ExecuteTemplate(w, "check_config_fragment.html", map[string]string{"Type": typ})
}

func (s *Server) handleHCURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	host := r.FormValue("host")
	url := r.FormValue("url")
	action := r.FormValue("action")
	if action == "clear" {
		url = ""
	}
	log.Printf("HCURL update request: host=%q url=%q", host, url)
	s.st.SetHCURL(host, url)
	fmt.Fprint(w, hcurlSection(host, url))
}

func hcurlSection(host, url string) string {
	return fmt.Sprintf(`
	<div class="field has-addons">
	  <div class="control is-expanded">
	    <input class="input" type="text" name="url" placeholder="Healthchecks.io ping URL" value="%s">
	  </div>
	  <div class="control">
	    <button class="button is-link" hx-post="/hcurl" hx-include="closest .field" hx-vals='{"host":"%s"}' hx-target="#hc-%s" hx-swap="outerHTML">Save</button>
	  </div>
	  <div class="control">
	    <button class="button is-light is-danger" hx-post="/hcurl" hx-vals='{"host":"%s","action":"clear"}' hx-target="#hc-%s" hx-swap="outerHTML">Clear</button>
	  </div>
	</div>`, template.HTMLEscapeString(url), host, host, host, host)
}

func (s *Server) handleAddHTTPForm(w http.ResponseWriter, r *http.Request) {
	host := r.FormValue("host")
	_ = s.tpl.ExecuteTemplate(w, "addhttp_modal.html", map[string]string{"Host": host})
}

func (s *Server) handleAddHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	host := r.FormValue("host")
	url := r.FormValue("url")
	expectStr := r.FormValue("expect")
	id := r.FormValue("id")
	dependsOn := r.FormValue("depends_on")
	mqttNotify := r.FormValue("mqtt_notify") == "true"
	expect := 200
	if expectStr != "" {
		if v, err := strconv.Atoi(expectStr); err == nil {
			expect = v
		}
	}
	if err := s.st.AddHTTPCheck(host, url, expect, id, dependsOn, mqttNotify); err != nil {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	data := struct{ Hosts []*state.HostStatus }{Hosts: s.st.Snapshot()}
	_ = s.tpl.ExecuteTemplate(w, "add_host_result.html", data)
}

func (s *Server) handleEditAddCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	host := r.FormValue("host")
	typ := r.FormValue("type")
	url := r.FormValue("url")
	expectStr := r.FormValue("expect")
	portStr := r.FormValue("port")
	id := r.FormValue("id")
	dependsOn := r.FormValue("depends_on")
	mqttNotify := r.FormValue("mqtt_notify") == "true"
	expect := 200
	if expectStr != "" {
		if v, err := strconv.Atoi(expectStr); err == nil {
			expect = v
		}
	}
	port := 0
	if portStr != "" {
		if v, err := strconv.Atoi(portStr); err == nil {
			port = v
		}
	}
	var err error
	switch typ {
	case "ping":
		err = s.st.AddPingCheck(host, id, dependsOn, mqttNotify)
	case "http":
		err = s.st.AddHTTPCheck(host, url, expect, id, dependsOn, mqttNotify)
	case "tcp":
		err = s.st.AddTCPCheck(host, port, id, dependsOn, mqttNotify)
	default:
		err = fmt.Errorf("unknown type")
	}
	if err != nil {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	hs, _ := s.st.GetHost(host)
	_ = s.tpl.ExecuteTemplate(w, "edithost_modal.html", hs)
}

func (s *Server) handleEditDelCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	host := r.FormValue("host")
	idxStr := r.FormValue("idx")
	idx, _ := strconv.Atoi(idxStr)
	if err := s.st.RemoveCheck(host, idx); err != nil {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	hs, _ := s.st.GetHost(host)
	_ = s.tpl.ExecuteTemplate(w, "edithost_modal.html", hs)
}

func (s *Server) handleEditSaveChecks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	host := r.FormValue("host")
	countStr := r.FormValue("check_count")
	count, _ := strconv.Atoi(countStr)

	for i := 0; i < count; i++ {
		typ := r.FormValue(fmt.Sprintf("type_%d", i))
		id := r.FormValue(fmt.Sprintf("id_%d", i))
		dependsOn := r.FormValue(fmt.Sprintf("depends_on_%d", i))
		mqttNotify := r.FormValue(fmt.Sprintf("mqtt_notify_%d", i)) == "true"

		if typ == "http" {
			url := r.FormValue(fmt.Sprintf("url_%d", i))
			expectStr := r.FormValue(fmt.Sprintf("expect_%d", i))
			expect := 200
			if expectStr != "" {
				if v, err := strconv.Atoi(expectStr); err == nil {
					expect = v
				}
			}
			_ = s.st.UpdateHTTPCheck(host, i, url, expect, id, dependsOn, mqttNotify)
		} else if typ == "tcp" {
			portStr := r.FormValue(fmt.Sprintf("port_%d", i))
			port := 0
			if portStr != "" {
				if v, err := strconv.Atoi(portStr); err == nil {
					port = v
				}
			}
			_ = s.st.UpdateTCPCheck(host, i, port, id, dependsOn, mqttNotify)
		} else {
			// For ping checks, just update the dependencies
			_ = s.st.UpdateCheckDependencies(host, i, id, dependsOn, mqttNotify)
		}
	}
	hs, _ := s.st.GetHost(host)
	_ = s.tpl.ExecuteTemplate(w, "edithost_modal.html", hs)
}

func (s *Server) handleEditUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	host := r.FormValue("host")
	idxStr := r.FormValue("idx")
	idx, _ := strconv.Atoi(idxStr)
	url := r.FormValue("url")
	expectStr := r.FormValue("expect")
	id := r.FormValue("id")
	dependsOn := r.FormValue("depends_on")
	mqttNotify := r.FormValue("mqtt_notify") == "true"
	expect := 200
	if expectStr != "" {
		if v, err := strconv.Atoi(expectStr); err == nil {
			expect = v
		}
	}
	if err := s.st.UpdateHTTPCheck(host, idx, url, expect, id, dependsOn, mqttNotify); err != nil {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	hs, _ := s.st.GetHost(host)
	_ = s.tpl.ExecuteTemplate(w, "edithost_modal.html", hs)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Stats state.AggregateStats
	}{
		Stats: s.st.GetAggregateStats(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	format := r.FormValue("format")
	if format == "compact" {
		_ = s.tpl.ExecuteTemplate(w, "stats_compact.html", data)
	} else {
		_ = s.tpl.ExecuteTemplate(w, "stats.html", data)
	}
}

func (s *Server) handleSilenceAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	s.st.SetAllEnabled(false)
	data := struct{ Hosts []*state.HostStatus }{Hosts: s.st.Snapshot()}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tpl.ExecuteTemplate(w, "hosts.html", data)
}

func (s *Server) handleEnableAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	s.st.SetAllEnabled(true)
	data := struct{ Hosts []*state.HostStatus }{Hosts: s.st.Snapshot()}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tpl.ExecuteTemplate(w, "hosts.html", data)
}

func toggleButton(host string, idx int, enabled bool) string {
	if enabled {
		return fmt.Sprintf(`<button class="check-toggle disable" hx-post="/toggle" hx-vals='{"host":"%s","idx":"%d","enabled":"false"}' hx-target="this" hx-swap="outerHTML">Disable</button>`, host, idx)
	}
	return fmt.Sprintf(`<button class="check-toggle enable" hx-post="/toggle" hx-vals='{"host":"%s","idx":"%d","enabled":"true"}' hx-target="this" hx-swap="outerHTML">Enable</button>`, host, idx)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

// generateSparklineSVG creates an inline SVG sparkline chart from latency history
func generateSparklineSVG(history []int64, isOK bool) template.HTML {
	if len(history) == 0 {
		return template.HTML("")
	}

	width := 100
	height := 24
	padding := 2

	// Find max value for scaling (minimum 1 to avoid division by zero)
	maxVal := int64(1)
	for _, v := range history {
		if v > maxVal {
			maxVal = v
		}
	}

	// Build SVG path
	points := make([]string, 0, len(history))
	chartWidth := float64(width - 2*padding)
	chartHeight := float64(height - 2*padding)

	for i, v := range history {
		x := float64(padding) + (float64(i)/float64(len(history)-1))*chartWidth
		if len(history) == 1 {
			x = float64(width) / 2
		}
		// Invert Y since SVG origin is top-left
		y := float64(height-padding) - (float64(v)/float64(maxVal))*chartHeight
		if i == 0 {
			points = append(points, fmt.Sprintf("M%.1f,%.1f", x, y))
		} else {
			points = append(points, fmt.Sprintf("L%.1f,%.1f", x, y))
		}
	}

	// Determine color based on current check status
	lineColor := "#22c55e" // green by default
	if !isOK {
		lineColor = "#ef4444" // red if check is currently down
	}

	// Build filled area path (for gradient effect)
	areaPoints := make([]string, 0, len(history)+2)
	for i, v := range history {
		x := float64(padding) + (float64(i)/float64(len(history)-1))*chartWidth
		if len(history) == 1 {
			x = float64(width) / 2
		}
		y := float64(height-padding) - (float64(v)/float64(maxVal))*chartHeight
		if i == 0 {
			areaPoints = append(areaPoints, fmt.Sprintf("M%.1f,%.1f", x, y))
		} else {
			areaPoints = append(areaPoints, fmt.Sprintf("L%.1f,%.1f", x, y))
		}
	}
	// Close the area path
	lastX := float64(padding) + chartWidth
	if len(history) == 1 {
		lastX = float64(width) / 2
	}
	areaPoints = append(areaPoints, fmt.Sprintf("L%.1f,%d", lastX, height-padding))
	areaPoints = append(areaPoints, fmt.Sprintf("L%d,%d", padding, height-padding))
	areaPoints = append(areaPoints, "Z")

	svg := fmt.Sprintf(`<svg class="sparkline" width="%d" height="%d" viewBox="0 0 %d %d">
		<defs>
			<linearGradient id="sparkGrad" x1="0%%" y1="0%%" x2="0%%" y2="100%%">
				<stop offset="0%%" style="stop-color:%s;stop-opacity:0.3"/>
				<stop offset="100%%" style="stop-color:%s;stop-opacity:0.05"/>
			</linearGradient>
		</defs>
		<path d="%s" fill="url(#sparkGrad)" />
		<path d="%s" fill="none" stroke="%s" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
	</svg>`,
		width, height, width, height,
		lineColor, lineColor,
		joinStrings(areaPoints),
		joinStrings(points),
		lineColor)

	return template.HTML(svg)
}

func joinStrings(s []string) string {
	result := ""
	for _, str := range s {
		result += str
	}
	return result
}

// generateDonutChartSVG creates a donut/ring chart showing up/down/disabled percentages
func generateDonutChartSVG(stats state.AggregateStats) template.HTML {
	size := 120
	strokeWidth := 12
	radius := (size - strokeWidth) / 2
	center := size / 2
	circumference := 2 * math.Pi * float64(radius)

	total := stats.ChecksUp + stats.ChecksDown + stats.ChecksParentFailed + stats.ChecksDisabled + stats.ChecksUnknown
	if total == 0 {
		return template.HTML(fmt.Sprintf(`<svg width="%d" height="%d" viewBox="0 0 %d %d">
			<circle cx="%d" cy="%d" r="%d" fill="none" stroke="#334155" stroke-width="%d"/>
			<text x="%d" y="%d" text-anchor="middle" dominant-baseline="middle" fill="#64748b" font-size="12">No data</text>
		</svg>`, size, size, size, size, center, center, radius, strokeWidth, center, center))
	}

	// Calculate segment sizes based on CURRENT status (for visual ring)
	upPct := float64(stats.ChecksUp) / float64(total)
	downPct := float64(stats.ChecksDown) / float64(total)
	parentFailedPct := float64(stats.ChecksParentFailed) / float64(total)
	disabledPct := float64(stats.ChecksDisabled) / float64(total)
	unknownPct := float64(stats.ChecksUnknown) / float64(total)

	upLen := circumference * upPct
	downLen := circumference * downPct
	parentFailedLen := circumference * parentFailedPct
	disabledLen := circumference * disabledPct
	unknownLen := circumference * unknownPct

	upOffset := 0.0
	downOffset := -upLen
	parentFailedOffset := -upLen - downLen
	disabledOffset := -upLen - downLen - parentFailedLen
	unknownOffset := -upLen - downLen - parentFailedLen - disabledLen

	// Use historical uptime for the center percentage
	// If no historical data yet, show current status percentage
	displayPct := stats.OverallUptime
	if displayPct == 0 && stats.ChecksUp > 0 {
		// No historical data yet, show current percentage
		displayPct = float64(stats.ChecksUp) / float64(stats.ChecksUp+stats.ChecksDown) * 100
	}

	// Choose center text color based on health
	textColor := "#22c55e" // green
	if stats.ChecksDown > 0 {
		textColor = "#ef4444" // red
	} else if stats.ChecksParentFailed > 0 {
		textColor = "#f97316" // orange for parent-failed
	} else if displayPct < 99 {
		textColor = "#f59e0b" // amber
	}

	svg := fmt.Sprintf(`<svg width="%d" height="%d" viewBox="0 0 %d %d" class="donut-chart">
		<circle cx="%d" cy="%d" r="%d" fill="none" stroke="#1e293b" stroke-width="%d"/>
		<circle cx="%d" cy="%d" r="%d" fill="none" stroke="#22c55e" stroke-width="%d" 
			stroke-dasharray="%.1f %.1f" stroke-dashoffset="%.1f" transform="rotate(-90 %d %d)"/>
		<circle cx="%d" cy="%d" r="%d" fill="none" stroke="#ef4444" stroke-width="%d"
			stroke-dasharray="%.1f %.1f" stroke-dashoffset="%.1f" transform="rotate(-90 %d %d)"/>
		<circle cx="%d" cy="%d" r="%d" fill="none" stroke="#f97316" stroke-width="%d"
			stroke-dasharray="%.1f %.1f" stroke-dashoffset="%.1f" transform="rotate(-90 %d %d)"/>
		<circle cx="%d" cy="%d" r="%d" fill="none" stroke="#64748b" stroke-width="%d"
			stroke-dasharray="%.1f %.1f" stroke-dashoffset="%.1f" transform="rotate(-90 %d %d)"/>
		<circle cx="%d" cy="%d" r="%d" fill="none" stroke="#f59e0b" stroke-width="%d"
			stroke-dasharray="%.1f %.1f" stroke-dashoffset="%.1f" transform="rotate(-90 %d %d)"/>
		<text x="%d" y="%d" text-anchor="middle" dominant-baseline="middle" fill="%s" font-size="20" font-weight="600">%.1f%%</text>
		<text x="%d" y="%d" text-anchor="middle" dominant-baseline="middle" fill="#64748b" font-size="10">uptime</text>
	</svg>`,
		size, size, size, size,
		center, center, radius, strokeWidth,
		center, center, radius, strokeWidth, upLen, circumference, upOffset, center, center,
		center, center, radius, strokeWidth, downLen, circumference, downOffset, center, center,
		center, center, radius, strokeWidth, parentFailedLen, circumference, parentFailedOffset, center, center, // Orange for parent-failed
		center, center, radius, strokeWidth, disabledLen, circumference, disabledOffset, center, center,
		center, center, radius, strokeWidth, unknownLen, circumference, unknownOffset, center, center,
		center, center-4, textColor, displayPct,
		center, center+14)

	return template.HTML(svg)
}

// generateHeatmapSVG creates a heatmap grid showing recent check results
func generateHeatmapSVG(data []bool) template.HTML {
	if len(data) == 0 {
		return template.HTML("")
	}

	// Use smaller cells and single row for compact display
	cellSize := 4
	gap := 1
	cols := len(data) // Single row
	if cols > 30 {
		cols = 30 // Max 30 cells
		data = data[len(data)-30:]
	}

	width := cols*(cellSize+gap) - gap
	height := cellSize

	var cells string
	for i, ok := range data {
		x := i * (cellSize + gap)
		color := "#22c55e"
		if !ok {
			color = "#ef4444"
		}
		cells += fmt.Sprintf(`<rect x="%d" y="0" width="%d" height="%d" rx="1" fill="%s"/>`, x, cellSize, cellSize, color)
	}

	return template.HTML(fmt.Sprintf(`<svg width="%d" height="%d" viewBox="0 0 %d %d" class="heatmap">%s</svg>`, width, height, width, height, cells))
}

// generateUptimeBarSVG creates a horizontal bar showing uptime percentage
func generateUptimeBarSVG(uptime float64) template.HTML {
	width := 100
	height := 8

	color := "#22c55e"
	if uptime < 99 {
		color = "#f59e0b"
	}
	if uptime < 95 {
		color = "#ef4444"
	}

	fillWidth := int(float64(width) * uptime / 100)

	return template.HTML(fmt.Sprintf(`<svg width="%d" height="%d" viewBox="0 0 %d %d" class="uptime-bar">
		<rect x="0" y="0" width="%d" height="%d" rx="4" fill="#1e293b"/>
		<rect x="0" y="0" width="%d" height="%d" rx="4" fill="%s"/>
	</svg>`, width, height, width, height, width, height, fillWidth, height, color))
}

// generateSmokepingChartSVG creates a smokeping-style latency chart
func generateSmokepingChartSVG(history []state.CheckDataPoint, width, height int) template.HTML {
	if len(history) == 0 {
		return template.HTML(fmt.Sprintf(`<svg width="%d" height="%d" viewBox="0 0 %d %d">
			<text x="%d" y="%d" text-anchor="middle" dominant-baseline="middle" fill="#64748b" font-size="12">No data yet</text>
		</svg>`, width, height, width, height, width/2, height/2))
	}

	// Scale padding based on chart size
	paddingX := 35
	paddingY := 20
	if height < 150 {
		paddingY = 15
	}
	chartWidth := width - 2*paddingX
	chartHeight := height - 2*paddingY

	// Find max latency for scaling
	maxLatency := int64(1)
	for _, dp := range history {
		if dp.LatencyMS > maxLatency {
			maxLatency = dp.LatencyMS
		}
	}
	// Add 20% headroom
	maxLatency = int64(float64(maxLatency) * 1.2)
	if maxLatency < 10 {
		maxLatency = 10
	}

	// Group data points into buckets for percentile calculation
	bucketCount := chartWidth / 3 // One bucket per 3 pixels
	if bucketCount > len(history) {
		bucketCount = len(history)
	}
	if bucketCount < 1 {
		bucketCount = 1
	}

	type bucket struct {
		min, max, median, p75, p95 int64
		hasData                    bool
		hasFailure                 bool
	}

	buckets := make([]bucket, bucketCount)

	for bi := 0; bi < bucketCount; bi++ {
		start := bi * len(history) / bucketCount
		end := (bi + 1) * len(history) / bucketCount
		if end > len(history) {
			end = len(history)
		}

		var latencies []int64
		for i := start; i < end; i++ {
			if !history[i].OK {
				buckets[bi].hasFailure = true
			}
			if history[i].LatencyMS > 0 {
				latencies = append(latencies, history[i].LatencyMS)
			}
		}

		if len(latencies) > 0 {
			buckets[bi].hasData = true
			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			buckets[bi].min = latencies[0]
			buckets[bi].max = latencies[len(latencies)-1]
			buckets[bi].median = latencies[len(latencies)/2]
			buckets[bi].p75 = latencies[int(float64(len(latencies))*0.75)]
			p95Idx := int(float64(len(latencies)) * 0.95)
			if p95Idx >= len(latencies) {
				p95Idx = len(latencies) - 1
			}
			buckets[bi].p95 = latencies[p95Idx]
		}
	}

	// Build SVG
	var svg string
	svg += fmt.Sprintf(`<svg width="%d" height="%d" viewBox="0 0 %d %d" class="smokeping-chart">`, width, height, width, height)

	// Background
	svg += fmt.Sprintf(`<rect x="0" y="0" width="%d" height="%d" fill="#0f172a"/>`, width, height)

	// Grid lines - keep 5 for good granularity
	gridLines := 5
	for i := 0; i <= gridLines; i++ {
		y := paddingY + i*chartHeight/gridLines
		latencyVal := maxLatency - int64(i)*maxLatency/int64(gridLines)
		svg += fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#1e293b" stroke-width="0.5"/>`, paddingX, y, paddingX+chartWidth, y)
		svg += fmt.Sprintf(`<text x="%d" y="%d" text-anchor="end" fill="#64748b" font-size="7">%dms</text>`, paddingX-2, y+2, latencyVal)
	}

	// X-axis labels
	if len(history) > 0 {
		first := history[0].Timestamp.Format("15:04")
		last := history[len(history)-1].Timestamp.Format("15:04")
		svg += fmt.Sprintf(`<text x="%d" y="%d" text-anchor="start" fill="#64748b" font-size="7">%s</text>`, paddingX, height-2, first)
		svg += fmt.Sprintf(`<text x="%d" y="%d" text-anchor="end" fill="#64748b" font-size="7">%s</text>`, paddingX+chartWidth, height-2, last)
	}

	// Draw smokeping-style bands
	bucketWidth := float64(chartWidth) / float64(bucketCount)

	// P95 to Max band (lightest)
	var maxPoints, p95PointsRevForMax []string
	for bi, b := range buckets {
		if !b.hasData {
			continue
		}
		x := float64(paddingX) + float64(bi)*bucketWidth + bucketWidth/2
		yMax := float64(paddingY) + float64(chartHeight)*(1-float64(b.max)/float64(maxLatency))
		yP95 := float64(paddingY) + float64(chartHeight)*(1-float64(b.p95)/float64(maxLatency))
		maxPoints = append(maxPoints, fmt.Sprintf("%.1f,%.1f", x, yMax))
		p95PointsRevForMax = append([]string{fmt.Sprintf("%.1f,%.1f", x, yP95)}, p95PointsRevForMax...)
	}
	if len(maxPoints) > 0 {
		svg += fmt.Sprintf(`<polygon points="%s %s" fill="rgba(59, 130, 246, 0.1)"/>`,
			joinStrings2(maxPoints, " "), joinStrings2(p95PointsRevForMax, " "))
	}

	// P75 to P95 band
	var p75Points, p95PointsRev []string
	for bi, b := range buckets {
		if !b.hasData {
			continue
		}
		x := float64(paddingX) + float64(bi)*bucketWidth + bucketWidth/2
		yP75 := float64(paddingY) + float64(chartHeight)*(1-float64(b.p75)/float64(maxLatency))
		yP95 := float64(paddingY) + float64(chartHeight)*(1-float64(b.p95)/float64(maxLatency))
		p75Points = append(p75Points, fmt.Sprintf("%.1f,%.1f", x, yP75))
		p95PointsRev = append([]string{fmt.Sprintf("%.1f,%.1f", x, yP95)}, p95PointsRev...)
	}
	if len(p75Points) > 0 {
		svg += fmt.Sprintf(`<polygon points="%s %s" fill="rgba(59, 130, 246, 0.2)"/>`,
			joinStrings2(p75Points, " "), joinStrings2(p95PointsRev, " "))
	}

	// Median to P75 band
	var medianPoints, p75PointsRev []string
	for bi, b := range buckets {
		if !b.hasData {
			continue
		}
		x := float64(paddingX) + float64(bi)*bucketWidth + bucketWidth/2
		yMedian := float64(paddingY) + float64(chartHeight)*(1-float64(b.median)/float64(maxLatency))
		yP75 := float64(paddingY) + float64(chartHeight)*(1-float64(b.p75)/float64(maxLatency))
		medianPoints = append(medianPoints, fmt.Sprintf("%.1f,%.1f", x, yMedian))
		p75PointsRev = append([]string{fmt.Sprintf("%.1f,%.1f", x, yP75)}, p75PointsRev...)
	}
	if len(medianPoints) > 0 {
		svg += fmt.Sprintf(`<polygon points="%s %s" fill="rgba(59, 130, 246, 0.4)"/>`,
			joinStrings2(medianPoints, " "), joinStrings2(p75PointsRev, " "))
	}

	// Min to Median band (darkest)
	var minPoints, medianPointsRev []string
	for bi, b := range buckets {
		if !b.hasData {
			continue
		}
		x := float64(paddingX) + float64(bi)*bucketWidth + bucketWidth/2
		yMin := float64(paddingY) + float64(chartHeight)*(1-float64(b.min)/float64(maxLatency))
		yMedian := float64(paddingY) + float64(chartHeight)*(1-float64(b.median)/float64(maxLatency))
		minPoints = append(minPoints, fmt.Sprintf("%.1f,%.1f", x, yMin))
		medianPointsRev = append([]string{fmt.Sprintf("%.1f,%.1f", x, yMedian)}, medianPointsRev...)
	}
	if len(minPoints) > 0 {
		svg += fmt.Sprintf(`<polygon points="%s %s" fill="rgba(59, 130, 246, 0.6)"/>`,
			joinStrings2(minPoints, " "), joinStrings2(medianPointsRev, " "))
	}

	// Median line
	var medianPath string
	for bi, b := range buckets {
		if !b.hasData {
			continue
		}
		x := float64(paddingX) + float64(bi)*bucketWidth + bucketWidth/2
		y := float64(paddingY) + float64(chartHeight)*(1-float64(b.median)/float64(maxLatency))
		if medianPath == "" {
			medianPath = fmt.Sprintf("M%.1f,%.1f", x, y)
		} else {
			medianPath += fmt.Sprintf(" L%.1f,%.1f", x, y)
		}
	}
	if medianPath != "" {
		svg += fmt.Sprintf(`<path d="%s" fill="none" stroke="#3b82f6" stroke-width="1"/>`, medianPath)
	}

	// Packet loss markers (red dots)
	for bi, b := range buckets {
		if b.hasFailure {
			x := float64(paddingX) + float64(bi)*bucketWidth + bucketWidth/2
			y := float64(paddingY + chartHeight - 2)
			svg += fmt.Sprintf(`<circle cx="%.1f" cy="%.1f" r="1.5" fill="#ef4444"/>`, x, y)
		}
	}

	svg += `</svg>`
	return template.HTML(svg)
}

func joinStrings2(s []string, sep string) string {
	result := ""
	for i, str := range s {
		if i > 0 {
			result += sep
		}
		result += str
	}
	return result
}

func formatUptime(uptime float64) string {
	if uptime >= 99.99 {
		return fmt.Sprintf("%.2f%%", uptime)
	} else if uptime >= 99.9 {
		return fmt.Sprintf("%.2f%%", uptime)
	} else if uptime >= 99 {
		return fmt.Sprintf("%.1f%%", uptime)
	}
	return fmt.Sprintf("%.1f%%", uptime)
}

func healthScoreColor(score int) string {
	if score >= 95 {
		return "#22c55e"
	} else if score >= 80 {
		return "#f59e0b"
	}
	return "#ef4444"
}

func healthScoreColorWithBlocked(score int, hasBlocked bool) string {
	if hasBlocked {
		return "#f97316" // orange for blocked
	}
	return healthScoreColor(score)
}

// calculateCheckUptime calculates uptime percentage from CheckStatus
func calculateCheckUptime(c state.CheckStatus) float64 {
	if c.TotalChecks == 0 {
		return 100.0
	}
	return float64(c.SuccessChecks) / float64(c.TotalChecks) * 100
}

// extractHeatmapData extracts last 30 results from full history for heatmap display
func extractHeatmapData(history []state.CheckDataPoint) []bool {
	heatmapSize := 30
	startIdx := len(history) - heatmapSize
	if startIdx < 0 {
		startIdx = 0
	}
	result := make([]bool, 0, heatmapSize)
	for i := startIdx; i < len(history); i++ {
		result = append(result, history[i].OK)
	}
	return result
}

// Analytics handlers
func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Hosts  []state.HostAnalytics
		Stats  state.AggregateStats
		Events []state.Event
	}{
		Hosts:  s.st.GetAllHostAnalytics(),
		Stats:  s.st.GetAggregateStats(),
		Events: state.GetEvents(20),
	}
	_ = s.tpl.ExecuteTemplate(w, "analytics.html", data)
}

func (s *Server) handleHostAnalytics(w http.ResponseWriter, r *http.Request) {
	hostName := r.FormValue("host")
	analytics, ok := s.st.GetHostAnalytics(hostName)
	if !ok {
		w.WriteHeader(404)
		return
	}
	_ = s.tpl.ExecuteTemplate(w, "host_analytics.html", analytics)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	events := state.GetEvents(50)
	data := struct{ Events []state.Event }{Events: events}
	_ = s.tpl.ExecuteTemplate(w, "events.html", data)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	mqttSettings := s.st.GetMQTTSettings()
	data := struct {
		MQTT          config.MQTTSettings
		MQTTConnected bool
	}{
		MQTT:          mqttSettings,
		MQTTConnected: s.st.IsMQTTConnected(),
	}
	_ = s.tpl.ExecuteTemplate(w, "settings.html", data)
}

func (s *Server) handleSettingsMQTT(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}

	enabled := r.FormValue("mqtt_enabled") == "true"
	broker := r.FormValue("mqtt_broker")
	clientID := r.FormValue("mqtt_client_id")
	username := r.FormValue("mqtt_username")
	password := r.FormValue("mqtt_password")
	topic := r.FormValue("mqtt_topic")

	settings := config.MQTTSettings{
		Enabled:  enabled,
		Broker:   broker,
		ClientID: clientID,
		Username: username,
		Password: password,
		Topic:    topic,
	}

	if err := s.st.UpdateMQTTSettings(settings); err != nil {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(fmt.Sprintf(`<div class="alert alert-error">Error saving settings: %s</div>`, err.Error())))
		return
	}

	// Return success message
	_, _ = w.Write([]byte(`<div class="alert alert-success">MQTT settings saved successfully. <a href="/settings" style="color: inherit; text-decoration: underline;">Refresh</a> to see connection status.</div>`))
}
