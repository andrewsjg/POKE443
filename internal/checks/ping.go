//go:build !darwin


package checks

import (
	"time"
	ping "github.com/go-ping/ping"
)

func PingOnce(host string, timeout time.Duration) PingResult {
	p, err := ping.NewPinger(host)
	if err != nil {
		return PingResult{OK: false, Err: err}
	}
	p.Count = 1
	p.Timeout = timeout
	// Try privileged ICMP first; if it fails (e.g., no perms), fall back to unprivileged UDP.
	p.SetPrivileged(true)
	if err := p.Run(); err != nil {
		p.SetPrivileged(false)
		if err2 := p.Run(); err2 != nil {
			return PingResult{OK: false, Err: err2}
		}
	}
	stats := p.Statistics()
	ok := stats.PacketsRecv > 0
	lat := time.Duration(0)
	if ok {
		lat = stats.AvgRtt
	}
	return PingResult{OK: ok, Latency: lat, PacketsTx: stats.PacketsSent, PacketsRx: stats.PacketsRecv}
}
