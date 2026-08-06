package health

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

// Client sends HTTP requests through the NLB at a configurable interval,
// capturing per-request connection timing and server-reported state headers.
// Each request uses a new TCP connection (DisableKeepAlives) to match NLB
// per-connection routing behavior.
//
// Multiple worker goroutines run in parallel so that high-latency links
// (e.g., client in South America → NLB in us-east-1) don't bottleneck
// throughput — each worker fires independently on its own ticker.
type Client struct {
	targetURL  string
	interval   time.Duration
	workers    int
	httpClient *http.Client

	mu      sync.Mutex
	records []RequestRecord

	cancel context.CancelFunc
}

// NewClient creates a Client that polls the given URL at the given interval
// using numWorkers parallel goroutines. Each worker sends one request per
// interval tick independently, so effective throughput is approximately
// numWorkers / interval when RTT < interval, or numWorkers / RTT when
// RTT > interval.
func NewClient(targetURL string, interval time.Duration, numWorkers int) *Client {
	if numWorkers < 1 {
		numWorkers = 1
	}
	return &Client{
		targetURL: targetURL,
		interval:  interval,
		workers:   numWorkers,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives: true,
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			},
			Timeout: 10 * time.Second,
		},
	}
}

// Start begins sending requests in background goroutines (one per worker).
func (c *Client) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	for i := 0; i < c.workers; i++ {
		go c.pollLoop(ctx)
	}
}

// Stop cancels all background request goroutines.
func (c *Client) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
}

// Records returns a copy of all captured request records.
func (c *Client) Records() []RequestRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]RequestRecord, len(c.records))
	copy(result, c.records)
	return result
}

func (c *Client) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.doRequest(ctx)
		}
	}
}

func (c *Client) doRequest(ctx context.Context) {
	rec := RequestRecord{Timestamp: time.Now()}
	var dialStart time.Time

	trace := &httptrace.ClientTrace{
		ConnectStart: func(network, addr string) {
			dialStart = time.Now()
			if host, _, err := net.SplitHostPort(addr); err == nil {
				rec.TargetIP = host
			}
		},
		ConnectDone: func(network, addr string, err error) {
			if err == nil {
				rec.TCPDialDuration = time.Since(dialStart)
			}
		},
	}

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", c.targetURL, nil)
	if err != nil {
		rec.Error = fmt.Sprintf("create request: %v", err)
		c.addRecord(rec)
		return
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		rec.Error = fmt.Sprintf("request: %v", err)
		c.addRecord(rec)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	rec.HTTPStatus = resp.StatusCode
	rec.ServerState = resp.Header.Get("X-Server-State")
	rec.ServerID = resp.Header.Get("X-Server-ID")
	rec.FirstReadyzTime = resp.Header.Get("X-First-Readyz-Time")
	rec.IsNonReadyReq = rec.ServerState == "pre-readyz"

	c.addRecord(rec)
}

func (c *Client) addRecord(rec RequestRecord) {
	c.mu.Lock()
	c.records = append(c.records, rec)
	c.mu.Unlock()
}
