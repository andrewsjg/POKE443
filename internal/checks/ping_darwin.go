//go:build darwin

package checks

import (
	"context"
	"os/exec"
	"regexp"
	"time"
)

var timeRe = regexp.MustCompile(`time=([0-9]+\.?[0-9]*) ms`)

func PingOnce(host string, timeout time.Duration) PingResult {
	// Try to locate ping
	path, err := exec.LookPath("ping")
	if err != nil {
		// macOS usually has /sbin/ping
		path = "/sbin/ping"
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-c", "1", "-W", "2000", host)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return PingResult{OK: false, Err: ctx.Err()}
	}
	if err != nil {
		return PingResult{OK: false, Err: err}
	}
	lat := time.Duration(0)
	m := timeRe.FindStringSubmatch(string(out))
	if len(m) == 2 {
		// milliseconds to duration
		if v, perr := time.ParseDuration(m[1] + "ms"); perr == nil {
			lat = v
		}
	}
	return PingResult{OK: true, Latency: lat, PacketsTx: 1, PacketsRx: 1}
}
