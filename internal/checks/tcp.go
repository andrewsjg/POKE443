package checks

import (
	"fmt"
	"net"
	"time"
)

type TCPResult struct {
	Latency time.Duration
	OK      bool
	Err     error
}

// TCPCheck attempts to connect to a TCP port and returns the result
func TCPCheck(host string, port int, timeout time.Duration) TCPResult {
	addr := fmt.Sprintf("%s:%d", host, port)
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return TCPResult{OK: false, Err: err}
	}
	defer conn.Close()
	lat := time.Since(start)
	return TCPResult{OK: true, Latency: lat}
}
