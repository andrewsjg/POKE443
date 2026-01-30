package systray

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/getlantern/systray"
)

// MenuBar represents the system tray menu bar
type MenuBar struct {
	port       int
	onQuit     func()
	ctx        context.Context
	cancelFunc context.CancelFunc
}

// NewMenuBar creates a new menu bar instance
func NewMenuBar(port int, onQuit func()) *MenuBar {
	ctx, cancel := context.WithCancel(context.Background())
	return &MenuBar{
		port:       port,
		onQuit:     onQuit,
		ctx:        ctx,
		cancelFunc: cancel,
	}
}

// Run starts the system tray menu bar (blocking)
func (m *MenuBar) Run() {
	systray.Run(m.onReady, m.onExit)
}

// Stop stops the system tray
func (m *MenuBar) Stop() {
	m.cancelFunc()
	systray.Quit()
}

func (m *MenuBar) onReady() {
	// Set icon only (no title text)
	systray.SetTitle("")
	systray.SetTooltip("Health Checker - Monitoring Services")

	// Use a simple icon
	iconData := getIcon()
	if iconData != nil {
		systray.SetIcon(iconData)
	}

	// Create menu items
	mTitle := systray.AddMenuItem("POKE 443 - Infra Monitor", "")
	mTitle.Disable()
	systray.AddSeparator()
	mOpen := systray.AddMenuItem("Open Web Console", "Open the web interface in browser")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit the application")

	// Handle menu clicks
	go func() {
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-mOpen.ClickedCh:
				m.openWebUI()
			case <-mQuit.ClickedCh:
				log.Println("Quit requested from menu bar")
				if m.onQuit != nil {
					m.onQuit()
				}
				systray.Quit()
				return
			}
		}
	}()
}

func (m *MenuBar) onExit() {
	// Cleanup when systray exits
}

func (m *MenuBar) openWebUI() {
	url := fmt.Sprintf("http://localhost:%d", m.port)
	log.Printf("Opening web console: %s", url)

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		log.Printf("Unsupported platform for opening browser: %s", runtime.GOOS)
		return
	}

	if err := cmd.Start(); err != nil {
		log.Printf("Failed to open web console: %v", err)
	}
}

// getIcon returns an ECG icon for the menu bar
func getIcon() []byte {
	// Try to load icon from runtime file first (for easy customization)
	iconPaths := []string{
		"icon.png",
		"menubar-icon.png",
		filepath.Join("assets", "icon.png"),
	}

	for _, path := range iconPaths {
		if iconData, err := os.ReadFile(path); err == nil {
			log.Printf("Loaded menu bar icon from runtime file: %s", path)
			return iconData
		}
	}

	// Generate ECG icon programmatically
	log.Println("Using generated ECG menu bar icon")
	return generateECGIcon()
}

// generateECGIcon creates a simple ECG/heartbeat line icon
func generateECGIcon() []byte {
	const size = 22 // macOS menu bar icon size
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// ECG line color (white)
	ecgColor := color.RGBA{255, 255, 255, 255}

	// Draw ECG pattern: flat line, then spike up, down, up slightly, then flat
	// Y coordinates (0 is top, size-1 is bottom)
	midY := size / 2
	points := []struct{ x, y int }{
		{0, midY},
		{4, midY},        // flat start
		{6, midY - 2},    // small bump up
		{8, midY},        // back to middle
		{10, midY - 8},   // big spike up
		{12, midY + 6},   // big spike down
		{14, midY - 3},   // recovery up
		{16, midY},       // back to middle
		{size - 1, midY}, // flat end
	}

	// Draw thick line by drawing multiple adjacent lines
	for i := 0; i < len(points)-1; i++ {
		drawLine(img, points[i].x, points[i].y, points[i+1].x, points[i+1].y, ecgColor)
		// Make it thicker
		drawLine(img, points[i].x, points[i].y+1, points[i+1].x, points[i+1].y+1, ecgColor)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Printf("Failed to encode ECG icon: %v", err)
		return nil
	}
	return buf.Bytes()
}

// drawLine draws a line between two points using Bresenham's algorithm
func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx := abs(x1 - x0)
	dy := abs(y1 - y0)
	sx, sy := 1, 1
	if x0 >= x1 {
		sx = -1
	}
	if y0 >= y1 {
		sy = -1
	}
	err := dx - dy

	for {
		if x0 >= 0 && x0 < img.Bounds().Dx() && y0 >= 0 && y0 < img.Bounds().Dy() {
			img.SetRGBA(x0, y0, c)
		}
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x0 += sx
		}
		if e2 < dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
