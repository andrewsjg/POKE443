package checks

import (
	"net/http"
	"time"
)

type HTTPResult struct {
	Latency time.Duration
	Code    int
	Err     error
}

func HTTPGet(url string, timeout time.Duration) HTTPResult {
	client := &http.Client{Timeout: timeout}
	start := time.Now()
	resp, err := client.Get(url)
	if err != nil {
		return HTTPResult{Err: err}
	}
	defer resp.Body.Close()
	lat := time.Since(start)
	return HTTPResult{Latency: lat, Code: resp.StatusCode}
}
