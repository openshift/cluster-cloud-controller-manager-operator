package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func runClient(args []string) {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	url := fs.String("url", "", "NLB URL to send requests to (required)")
	interval := fs.Duration("interval", 100*time.Millisecond, "interval per worker")
	workers := fs.Int("workers", 8, "parallel request goroutines")
	port := fs.Int("port", 8080, "port to serve metrics/records API")
	aggregatorURL := fs.String("aggregator", "", "aggregator URL for pushing events")
	tlsInsecure := fs.Bool("tls-insecure", false, "skip TLS certificate verification for --url")
	fs.Parse(args)

	if *url == "" {
		fmt.Fprintf(os.Stderr, "client: --url is required\n")
		os.Exit(1)
	}

	log.Printf("[client] starting workers=%d interval=%s url=%s", *workers, *interval, *url)

	// Register with aggregator if configured.
	if *aggregatorURL != "" {
		podIP := os.Getenv("POD_IP")
		if podIP == "" {
			podIP = "localhost"
		}
		regBody, _ := json.Marshal(map[string]string{
			"role":      "client",
			"url":       fmt.Sprintf("http://%s:%d", podIP, *port),
			"server_id": "healthtest-client",
		})
		resp, err := http.Post(*aggregatorURL+"/register", "application/json", bytes.NewReader(regBody))
		if err != nil {
			log.Printf("[client] aggregator registration failed: %v", err)
		} else {
			resp.Body.Close()
			log.Printf("[client] registered with aggregator at %s", *aggregatorURL)
		}
		pushEvent(*aggregatorURL, Event{
			Source:    "client",
			Event:     "client_started",
			Timestamp: time.Now(),
		})
	}

	// Shared state: atomic counters.
	var (
		totalSent int64
		status2xx int64
		status4xx int64
		status5xx int64
		errors    int64
		preReadyz int64
	)

	// Records slice protected by mutex.
	var (
		recordsMu sync.Mutex
		records   []ClientRecord
	)

	// Per-server map protected by its own mutex.
	var (
		perServerMu sync.Mutex
		perServer   = make(map[string]PerServerCount)
	)

	// Stop channel to signal workers to stop sending.
	stopCh := make(chan struct{})

	// HTTP client that creates a new TCP connection for every request.
	transport := &http.Transport{DisableKeepAlives: true}
	if *tlsInsecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test-only self-signed cert
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	// sendRequest performs a single GET to the NLB URL with connection tracing.
	sendRequest := func() {
		var (
			targetIP  string
			dialStart time.Time
			dialDur   time.Duration
		)

		trace := &httptrace.ClientTrace{
			ConnectStart: func(network, addr string) {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					targetIP = addr
				} else {
					targetIP = host
				}
				dialStart = time.Now()
			},
			ConnectDone: func(network, addr string, err error) {
				dialDur = time.Since(dialStart)
			},
		}

		req, err := http.NewRequest("GET", *url, nil)
		if err != nil {
			log.Printf("[client] failed to create request: %v", err)
			return
		}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

		atomic.AddInt64(&totalSent, 1)

		now := time.Now()
		resp, err := httpClient.Do(req)

		rec := ClientRecord{
			Timestamp:       now,
			TargetIP:        targetIP,
			TCPDialDuration: dialDur,
		}

		if err != nil {
			atomic.AddInt64(&errors, 1)
			rec.Error = err.Error()

			recordsMu.Lock()
			records = append(records, rec)
			recordsMu.Unlock()
			return
		}
		defer resp.Body.Close()

		rec.HTTPStatus = resp.StatusCode
		rec.ServerState = resp.Header.Get("X-Server-State")
		rec.ServerID = resp.Header.Get("X-Server-ID")
		rec.ServerStartTime = resp.Header.Get("X-Server-Start-Time")
		rec.FirstReadyzTime = resp.Header.Get("X-First-Readyz-Time")

		// Classify status code.
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			atomic.AddInt64(&status2xx, 1)
		case resp.StatusCode >= 400 && resp.StatusCode < 500:
			atomic.AddInt64(&status4xx, 1)
		case resp.StatusCode >= 500 && resp.StatusCode < 600:
			atomic.AddInt64(&status5xx, 1)
		}

		// Detect pre-readyz response.
		if rec.ServerState == "pre-readyz" {
			rec.IsNonReadyReq = true
			atomic.AddInt64(&preReadyz, 1)

			pushEvent(*aggregatorURL, Event{
				Source:    "client",
				Event:    EventPreReadyz,
				ServerID: rec.ServerID,
				Detail:   fmt.Sprintf("target_ip=%s", rec.TargetIP),
				Timestamp: time.Now(),
			})
		}

		// Update per-server map.
		if rec.ServerID != "" {
			perServerMu.Lock()
			ps := perServer[rec.ServerID]
			ps.Total++
			if rec.IsNonReadyReq {
				ps.PreReadyz++
			}
			perServer[rec.ServerID] = ps
			perServerMu.Unlock()
		}

		recordsMu.Lock()
		records = append(records, rec)
		recordsMu.Unlock()
	}

	// Start worker goroutines.
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			ticker := time.NewTicker(*interval)
			defer ticker.Stop()
			for {
				select {
				case <-stopCh:
					return
				case <-ticker.C:
					sendRequest()
				}
			}
		}(i)
	}

	// Serve metrics, records, and healthz endpoints.
	mux := http.NewServeMux()

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		perServerMu.Lock()
		psCopy := make(map[string]PerServerCount, len(perServer))
		for k, v := range perServer {
			psCopy[k] = v
		}
		perServerMu.Unlock()

		m := ClientMetrics{
			Timestamp: time.Now(),
			Counters: ClientCounters{
				TotalSent: atomic.LoadInt64(&totalSent),
				Status2xx: atomic.LoadInt64(&status2xx),
				Status4xx: atomic.LoadInt64(&status4xx),
				Status5xx: atomic.LoadInt64(&status5xx),
				Errors:    atomic.LoadInt64(&errors),
				PreReadyz: atomic.LoadInt64(&preReadyz),
			},
			PerServer: psCopy,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(m)
	})

	mux.HandleFunc("/records", func(w http.ResponseWriter, r *http.Request) {
		recordsMu.Lock()
		recsCopy := make([]ClientRecord, len(records))
		copy(recsCopy, records)
		recordsMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(recsCopy)
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ok\n")
	})

	addr := fmt.Sprintf(":%d", *port)
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		log.Printf("[client] serving metrics/records on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[client] ListenAndServe failed: %v", err)
		}
	}()

	// Log stats periodically.
	go func() {
		for {
			time.Sleep(10 * time.Second)
			log.Printf("[client] sent=%d 2xx=%d 4xx=%d 5xx=%d err=%d pre_readyz=%d",
				atomic.LoadInt64(&totalSent),
				atomic.LoadInt64(&status2xx),
				atomic.LoadInt64(&status4xx),
				atomic.LoadInt64(&status5xx),
				atomic.LoadInt64(&errors),
				atomic.LoadInt64(&preReadyz),
			)
		}
	}()

	// Push metrics to aggregator periodically so it has client data
	// even if scraping fails.
	if *aggregatorURL != "" {
		go func() {
			for {
				time.Sleep(5 * time.Second)

				perServerMu.Lock()
				psCopy := make(map[string]PerServerCount, len(perServer))
				for k, v := range perServer {
					psCopy[k] = v
				}
				perServerMu.Unlock()

				m := ClientMetrics{
					Timestamp: time.Now(),
					Counters: ClientCounters{
						TotalSent: atomic.LoadInt64(&totalSent),
						Status2xx: atomic.LoadInt64(&status2xx),
						Status4xx: atomic.LoadInt64(&status4xx),
						Status5xx: atomic.LoadInt64(&status5xx),
						Errors:    atomic.LoadInt64(&errors),
						PreReadyz: atomic.LoadInt64(&preReadyz),
					},
					PerServer: psCopy,
				}
				metricsJSON, _ := json.Marshal(m)
				pushEvent(*aggregatorURL, Event{
					Source:    "client",
					Event:     "metrics_update",
					Detail:    string(metricsJSON),
					Timestamp: time.Now(),
				})
			}
		}()
	}

	// Handle SIGTERM: stop sending, keep serving for 60s, then exit.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	<-sigCh

	log.Printf("[client] received SIGTERM, stopping workers")
	close(stopCh)
	wg.Wait()
	log.Printf("[client] all workers stopped, keeping metrics server alive for 60s")

	time.Sleep(60 * time.Second)
	log.Printf("[client] grace period elapsed, exiting")
}
