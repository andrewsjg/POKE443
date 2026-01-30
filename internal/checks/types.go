package checks

import "time"

type PingResult struct {
	OK        bool
	Latency   time.Duration
	PacketsTx int
	PacketsRx int
	Err       error
}
