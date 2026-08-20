package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 19443, "service port")
	startupDelay := fs.Duration("startup-delay", 30*time.Second, "time before /readyz returns 200")
	aggregatorURL := fs.String("aggregator", "", "aggregator URL for pushing events")
	useTLS := fs.Bool("tls", false, "serve traffic and /readyz over TLS")
	tlsCert := fs.String("tls-cert", "", "path to TLS certificate PEM (required with --tls)")
	tlsKey := fs.String("tls-key", "", "path to TLS private key PEM (required with --tls)")
	fs.Parse(args)

	if *useTLS && (*tlsCert == "" || *tlsKey == "") {
		fmt.Fprintf(os.Stderr, "serve: --tls requires --tls-cert and --tls-key\n")
		os.Exit(1)
	}

	// Server identity: POD_NAME env var, fallback to hostname.
	serverID := os.Getenv("POD_NAME")
	if serverID == "" {
		h, err := os.Hostname()
		if err != nil {
			serverID = "unknown"
		} else {
			serverID = h
		}
	}

	processStart := time.Now()

	// Shared mutable state protected by mutex / atomics.
	var (
		mu             sync.Mutex
		state          = "pre-readyz"
		readyzReady    = false
		lifecycle      = Lifecycle{ProcessStart: &processStart}
		totalRequests  int64
		mainRequests   int64
		readyzRequests int64
		readyz200      int64
		readyz503      int64
	)

	getState := func() string {
		mu.Lock()
		defer mu.Unlock()
		return state
	}

	// GET / — main endpoint. Client requests go here via NLB.
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		count := atomic.AddInt64(&mainRequests, 1)
		atomic.AddInt64(&totalRequests, 1)

		// Log first request to confirm traffic is reaching the server
		if count == 1 {
			log.Printf("[serve] first main request from %s", r.RemoteAddr)
		}

		mu.Lock()
		s := state
		frt := lifecycle.FirstReadyz200
		mu.Unlock()

		w.Header().Set("X-Server-State", s)
		w.Header().Set("X-Server-ID", serverID)
		w.Header().Set("X-Server-Start-Time", processStart.Format(time.RFC3339Nano))
		if frt != nil {
			w.Header().Set("X-First-Readyz-Time", frt.Format(time.RFC3339Nano))
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ok\n")
	})

	// GET /readyz — health check endpoint. NLB HC probes and any direct
	// /readyz requests are counted here. If readyz_requests stays 0, the
	// NLB HC is going through kube-proxy's HC NodePort, not our port.
	http.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt64(&readyzRequests, 1)
		atomic.AddInt64(&totalRequests, 1)

		mu.Lock()
		ready := readyzReady
		mu.Unlock()

		// Log first few HC probes and then every 100th to confirm they arrive
		if count <= 3 || count%100 == 0 {
			log.Printf("[serve] /readyz probe #%d from %s ready=%v", count, r.RemoteAddr, ready)
		}

		if ready {
			atomic.AddInt64(&readyz200, 1)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "ok\n")
		} else {
			atomic.AddInt64(&readyz503, 1)
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "not ready\n")
		}
	})

	// TODO validate if it's deprecated since we change signals to ctl
	// POST /admin/readyz?ready=true|false — control readyz state
	http.HandleFunc("/admin/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		val := r.URL.Query().Get("ready")
		mu.Lock()
		switch val {
		case "true":
			readyzReady = true
			if state == "pre-readyz" || state == "draining" {
				state = "ready"
			}
			now := time.Now()
			if lifecycle.FirstReadyz200 == nil {
				lifecycle.FirstReadyz200 = &now
			}
			mu.Unlock()
			pushEvent(*aggregatorURL, Event{
				Source:    "server",
				ServerID: serverID,
				Event:    EventReadyzTrue,
				Timestamp: now,
			})
		case "false":
			readyzReady = false
			now := time.Now()
			lifecycle.ReadyzFalseAt = &now
			if state == "ready" {
				state = "draining"
			}
			mu.Unlock()
			pushEvent(*aggregatorURL, Event{
				Source:    "server",
				ServerID: serverID,
				Event:    EventReadyzFalse,
				Timestamp: now,
			})
		default:
			mu.Unlock()
			http.Error(w, "ready param must be true or false", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "readyz=%s\n", val)
	})

	// TODO validate if it's deprecated since we change signals to ctl
	// POST /admin/shutdown?delay=Ns — graceful shutdown
	http.HandleFunc("/admin/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		delayStr := r.URL.Query().Get("delay")
		delay := time.Duration(0)
		if delayStr != "" {
			d, err := time.ParseDuration(delayStr)
			if err != nil {
				http.Error(w, fmt.Sprintf("invalid delay: %v", err), http.StatusBadRequest)
				return
			}
			delay = d
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "shutting down in %s\n", delay)

		go func() {
			if delay > 0 {
				time.Sleep(delay)
			}
			mu.Lock()
			state = "shutdown"
			mu.Unlock()
			log.Printf("[serve] shutdown requested, exiting")
			os.Exit(0)
		}()
	})

	// GET /admin/lifecycle — JSON lifecycle timestamps
	http.HandleFunc("/admin/lifecycle", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lc := lifecycle
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(lc)
	})

	// GET /metrics — returns ServerMetrics JSON
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lc := lifecycle
		s := state
		mu.Unlock()

		m := ServerMetrics{
			ServerID:  serverID,
			State:     s,
			Timestamp: time.Now(),
			Lifecycle: lc,
			Counters: ServerCounters{
				TotalRequests:  atomic.LoadInt64(&totalRequests),
				MainRequests:   atomic.LoadInt64(&mainRequests),
				ReadyzRequests: atomic.LoadInt64(&readyzRequests),
				Readyz200:      atomic.LoadInt64(&readyz200),
				Readyz503:      atomic.LoadInt64(&readyz503),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(m)
	})

	// Start TCP listener.
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[serve] starting server id=%s on %s (startup-delay=%s)", serverID, addr, *startupDelay)

	// Record t_tcp_up and push event.
	now := time.Now()
	mu.Lock()
	lifecycle.TCPUp = &now
	mu.Unlock()

	pushEvent(*aggregatorURL, Event{
		Source:    "server",
		ServerID: serverID,
		Event:    EventTCPUp,
		Timestamp: now,
	})

	// Register with aggregator if configured.
	if *aggregatorURL != "" {
		podIP := os.Getenv("POD_IP")
		if podIP == "" {
			podIP = "localhost"
		}
		regURL := fmt.Sprintf("http://%s:%d", podIP, *port)
		regBody, _ := json.Marshal(map[string]string{"role": "server", "url": regURL, "server_id": serverID})
		resp, err := http.Post(*aggregatorURL+"/register", "application/json", bytes.NewReader(regBody))
		if err != nil {
			log.Printf("[serve] aggregator registration failed: %v", err)
		} else {
			resp.Body.Close()
			log.Printf("[serve] registered with aggregator at %s (url=%s)", *aggregatorURL, regURL)
		}
	}

	// Push server metrics to aggregator every 5 seconds so the aggregator
	// has server-side data even though it can't scrape servers directly
	// (control-plane SG blocks inbound from worker nodes).
	if *aggregatorURL != "" {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				mu.Lock()
				metrics := ServerMetrics{
					ServerID:  serverID,
					State:     state,
					Timestamp: time.Now().UTC(),
					Lifecycle: lifecycle,
					Counters: ServerCounters{
						TotalRequests:  atomic.LoadInt64(&totalRequests),
						MainRequests:   atomic.LoadInt64(&mainRequests),
						ReadyzRequests: atomic.LoadInt64(&readyzRequests),
						Readyz200:      atomic.LoadInt64(&readyz200),
						Readyz503:      atomic.LoadInt64(&readyz503),
					},
				}
				mu.Unlock()
				detail, _ := json.Marshal(metrics)
				pushEvent(*aggregatorURL, Event{
					Source:    "server",
					ServerID: serverID,
					Event:    "metrics_update",
					Detail:   string(detail),
					Timestamp: metrics.Timestamp,
				})
			}
		}()
	}

	// Schedule readyz transition after startup-delay.
	go func() {
		time.Sleep(*startupDelay)

		mu.Lock()
		// Only transition if we haven't been sigterm'd or manually set.
		if state == "pre-readyz" {
			readyzReady = true
			state = "ready"
			t := time.Now()
			lifecycle.FirstReadyz200 = &t
			mu.Unlock()

			log.Printf("[serve] startup-delay elapsed, readyz=true")
			pushEvent(*aggregatorURL, Event{
				Source:    "server",
				ServerID: serverID,
				Event:    EventReadyzTrue,
				Timestamp: t,
			})
		} else {
			mu.Unlock()
		}
	}()

	// Handle SIGTERM: set draining, readyz→503, but keep serving.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)

	signalReadyzFalse := func(fromSigterm bool) {
		now := time.Now()
		if fromSigterm {
			log.Printf("[serve] received SIGTERM, state→draining, readyz→503")
		} else {
			log.Printf("[serve] received SIGUSR1 (ctl readyz-false), readyz→503")
		}

		mu.Lock()
		readyzReady = false
		lifecycle.ReadyzFalseAt = &now
		if fromSigterm {
			state = "draining"
			lifecycle.Sigterm = &now
		} else if state == "ready" {
			state = "draining"
		}
		mu.Unlock()

		if fromSigterm {
			pushEvent(*aggregatorURL, Event{
				Source:    "server",
				ServerID:  serverID,
				Event:     EventSigterm,
				Timestamp: now,
			})
		}
		pushEvent(*aggregatorURL, Event{
			Source:    "server",
			ServerID:  serverID,
			Event:     EventReadyzFalse,
			Timestamp: now,
		})
	}

	go func() {
		<-sigCh
		signalReadyzFalse(true)
	}()

	// SIGUSR1/SIGUSR2 from e2e-nlb-health-test ctl (kubectl exec, same pod).
	ctlCh := make(chan os.Signal, 1)
	signal.Notify(ctlCh, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range ctlCh {
			switch sig {
			case syscall.SIGUSR1:
				signalReadyzFalse(false)
			case syscall.SIGUSR2:
				log.Printf("[serve] received SIGUSR2 (ctl restart), exiting for container restart")
				os.Exit(0)
			}
		}
	}()

	// Log state periodically.
	go func() {
		for {
			time.Sleep(10 * time.Second)
			log.Printf("[serve] state=%s total=%d main=%d readyz=%d (200=%d 503=%d)",
				getState(),
				atomic.LoadInt64(&totalRequests),
				atomic.LoadInt64(&mainRequests),
				atomic.LoadInt64(&readyzRequests),
				atomic.LoadInt64(&readyz200),
				atomic.LoadInt64(&readyz503),
			)
		}
	}()

	if *useTLS {
		log.Printf("[serve] TLS enabled (cert=%s key=%s)", *tlsCert, *tlsKey)
		if err := http.ListenAndServeTLS(addr, *tlsCert, *tlsKey, nil); err != nil {
			log.Fatalf("[serve] ListenAndServeTLS failed: %v", err)
		}
		return
	}
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("[serve] ListenAndServe failed: %v", err)
	}
}

// pushEvent sends a lifecycle event to the aggregator in a fire-and-forget goroutine.
func pushEvent(aggregatorURL string, evt Event) {
	if aggregatorURL == "" {
		return
	}
	go func() {
		data, _ := json.Marshal(evt)
		http.Post(aggregatorURL+"/event", "application/json", bytes.NewReader(data))
	}()
}
