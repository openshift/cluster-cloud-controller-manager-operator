package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// AgentInfo describes a registered agent (server or client).
type AgentInfo struct {
	Role     string `json:"role"`                // "server" or "client"
	URL      string `json:"url"`                 // base URL (e.g., http://10.0.22.243:19443)
	ServerID string `json:"server_id,omitempty"` // only for role="server"
}

// aggregator holds all mutable state for the aggregator process.
type aggregator struct {
	mu         sync.Mutex
	agents     map[string][]AgentInfo // role -> []AgentInfo
	events     []Event
	timeseries []TimeseriesRow
	latestTG   *TGSnapshot
}

func runAggregator(args []string) {
	fs := flag.NewFlagSet("aggregator", flag.ExitOnError)
	port := fs.Int("port", 8090, "port to serve aggregator API")
	scrapeInterval := fs.Duration("scrape-interval", 1*time.Second, "how often to scrape agent /metrics endpoints")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("aggregator: failed to parse flags: %v", err)
	}

	agg := &aggregator{
		agents: make(map[string][]AgentInfo),
	}

	mux := http.NewServeMux()

	// Receive endpoints
	mux.HandleFunc("/register", agg.handleRegister)
	mux.HandleFunc("/event", agg.handleEvent)
	mux.HandleFunc("/tg-snapshot", agg.handleTGSnapshot)

	// Serve endpoints
	mux.HandleFunc("/timeline", agg.handleTimeline)
	mux.HandleFunc("/timeseries", agg.handleTimeseries)
	mux.HandleFunc("/report", agg.handleReport)
	mux.HandleFunc("/healthz", handleHealthz)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: mux,
	}

	// Signal handling: on SIGTERM, stop scraping but keep serving for 60s.
	stopScrape := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("aggregator: received SIGTERM, stopping scrape loop")
		close(stopScrape)

		// Print shutdown summary.
		agg.mu.Lock()
		log.Printf("aggregator: === SHUTDOWN SUMMARY ===")
		log.Printf("aggregator: total events: %d", len(agg.events))
		log.Printf("aggregator: total timeseries rows: %d", len(agg.timeseries))
		// Print the last timeseries row if available
		if len(agg.timeseries) > 0 {
			last := agg.timeseries[len(agg.timeseries)-1]
			for sid, sm := range last.Servers {
				log.Printf("aggregator: server %s: state=%s reqs=%d readyz_reqs=%d", sid, sm.State, sm.Counters.TotalRequests, sm.Counters.ReadyzRequests)
			}
			if last.Client != nil {
				log.Printf("aggregator: client: total=%d 2xx=%d errors=%d pre_readyz=%d", last.Client.Counters.TotalSent, last.Client.Counters.Status2xx, last.Client.Counters.Errors, last.Client.Counters.PreReadyz)
			}
			if last.TG != nil {
				log.Printf("aggregator: tg: healthy=%d unhealthy=%d initial=%d", last.TG.HealthyCount, last.TG.UnhealthyCount, last.TG.InitialCount)
			}
		}
		// Print all events
		log.Printf("aggregator: === EVENT TIMELINE ===")
		for _, e := range agg.events {
			log.Printf("aggregator: %s source=%s server_id=%s event=%s", e.Timestamp.UTC().Format("15:04:05"), e.Source, e.ServerID, e.Event)
		}
		log.Printf("aggregator: === END SUMMARY ===")
		agg.mu.Unlock()

		// Keep serving for 60s so the test binary can fetch the final report.
		time.Sleep(60 * time.Second)
		log.Println("aggregator: grace period elapsed, shutting down HTTP server")
		server.Close()
	}()

	// Start scrape loop
	go agg.scrapeLoop(*scrapeInterval, stopScrape)

	log.Printf("aggregator: listening on :%d, scrape-interval=%s", *port, *scrapeInterval)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("aggregator: server error: %v", err)
	}
	log.Println("aggregator: shut down")
}

// ---------- scrape loop ----------

func (a *aggregator) scrapeLoop(interval time.Duration, stop <-chan struct{}) {
	client := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			log.Println("aggregator: scrape loop stopped")
			return
		case <-ticker.C:
			a.scrapeOnce(client)
		}
	}
}

func (a *aggregator) scrapeOnce(client *http.Client) {
	a.mu.Lock()
	servers := make([]AgentInfo, len(a.agents["server"]))
	copy(servers, a.agents["server"])
	clients := make([]AgentInfo, len(a.agents["client"]))
	copy(clients, a.agents["client"])
	latestTG := a.latestTG
	a.mu.Unlock()

	row := TimeseriesRow{
		Timestamp: time.Now().UTC(),
		Servers:   make(map[string]ServerMetrics),
	}

	// Servers push metrics via events (metrics_update) rather than being
	// scraped, because the control-plane security group blocks inbound
	// traffic from worker nodes on the healthserver port. Server metrics
	// arrive through pushEvent and are captured in the events list.
	_ = servers // registered but not scraped

	// Scrape client (take the first registered client)
	if len(clients) > 0 {
		cm, err := scrapeClientMetrics(client, clients[0].URL)
		if err != nil {
			log.Printf("aggregator: scrape client (%s): %v", clients[0].URL, err)
		} else {
			row.Client = cm
		}
	}

	// Attach latest TG snapshot
	if latestTG != nil {
		row.TG = latestTG
	}

	a.mu.Lock()
	a.timeseries = append(a.timeseries, row)
	a.mu.Unlock()
}

func scrapeServerMetrics(client *http.Client, baseURL string) (*ServerMetrics, error) {
	resp, err := client.Get(baseURL + "/metrics")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var sm ServerMetrics
	if err := json.NewDecoder(resp.Body).Decode(&sm); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &sm, nil
}

func scrapeClientMetrics(client *http.Client, baseURL string) (*ClientMetrics, error) {
	resp, err := client.Get(baseURL + "/metrics")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var cm ClientMetrics
	if err := json.NewDecoder(resp.Body).Decode(&cm); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &cm, nil
}

// ---------- receive handlers ----------

func (a *aggregator) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var info AgentInfo
	if err := json.NewDecoder(r.Body).Decode(&info); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if info.Role == "" || info.URL == "" {
		http.Error(w, "bad request: role and url are required", http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	a.agents[info.Role] = append(a.agents[info.Role], info)
	a.events = append(a.events, Event{
		Source:    info.Role,
		ServerID: info.ServerID,
		Event:    EventRegistered,
		Detail:   fmt.Sprintf("registered %s at %s", info.Role, info.URL),
		Timestamp: time.Now().UTC(),
	})
	a.mu.Unlock()

	log.Printf("aggregator: registered %s agent: %s (server_id=%s)", info.Role, info.URL, info.ServerID)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, `{"status":"ok"}`)
}

func (a *aggregator) handleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ev Event
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}

	a.mu.Lock()
	a.events = append(a.events, ev)
	a.mu.Unlock()

	// Log all events with raw detail (JSON preserved for aggregation).
	// For metrics_update, add a human-readable summary line after the raw log.
	log.Printf("aggregator: event: source=%s server_id=%s event=%s detail=%s",
		ev.Source, ev.ServerID, ev.Event, ev.Detail)

	if ev.Event == "metrics_update" && ev.Source == "server" {
		var sm ServerMetrics
		if json.Unmarshal([]byte(ev.Detail), &sm) == nil {
			log.Printf("aggregator: server %s: state=%s | service_reqs=%d | hc_reqs=%d (hc_200=%d hc_503=%d)",
				sm.ServerID, sm.State, sm.Counters.MainRequests,
				sm.Counters.ReadyzRequests, sm.Counters.Readyz200, sm.Counters.Readyz503)
		}
	} else if ev.Event == "metrics_update" && ev.Source == "client" {
		var cm ClientMetrics
		if json.Unmarshal([]byte(ev.Detail), &cm) == nil {
			log.Printf("aggregator: client: sent=%d 2xx=%d errors=%d pre_readyz=%d",
				cm.Counters.TotalSent, cm.Counters.Status2xx, cm.Counters.Errors, cm.Counters.PreReadyz)
		}
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, `{"status":"ok"}`)
}

func (a *aggregator) handleTGSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var snap TGSnapshot
	if err := json.NewDecoder(r.Body).Decode(&snap); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if snap.Timestamp.IsZero() {
		snap.Timestamp = time.Now().UTC()
	}

	a.mu.Lock()
	a.latestTG = &snap
	a.mu.Unlock()

	// Single-line TG snapshot with per-target state
	var parts []string
	for id, state := range snap.Targets {
		parts = append(parts, fmt.Sprintf("%s=%s", id, state))
	}
	log.Printf("aggregator: tg-snapshot: healthy=%d unhealthy=%d initial=%d | %s",
		snap.HealthyCount, snap.UnhealthyCount, snap.InitialCount, strings.Join(parts, ", "))
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, `{"status":"ok"}`)
}

// ---------- serve handlers ----------

func (a *aggregator) handleTimeline(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	events := make([]Event, len(a.events))
	copy(events, a.events)
	a.mu.Unlock()

	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

func (a *aggregator) handleTimeseries(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	ts := make([]TimeseriesRow, len(a.timeseries))
	copy(ts, a.timeseries)
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ts)
}

func (a *aggregator) handleReport(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	events := make([]Event, len(a.events))
	copy(events, a.events)
	tsCopy := make([]TimeseriesRow, len(a.timeseries))
	copy(tsCopy, a.timeseries)
	latestTG := a.latestTG
	a.mu.Unlock()

	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	var b strings.Builder
	b.WriteString("=== AGGREGATOR REPORT ===\n\n")

	// Events
	b.WriteString(fmt.Sprintf("--- Events (%d total) ---\n", len(events)))
	for _, ev := range events {
		b.WriteString(fmt.Sprintf("  [%s] src=%-8s server_id=%-20s event=%-22s detail=%s\n",
			ev.Timestamp.Format(time.RFC3339Nano),
			ev.Source, ev.ServerID, ev.Event, ev.Detail))
	}
	b.WriteString("\n")

	// Latest metrics from the most recent timeseries row
	if len(tsCopy) > 0 {
		latest := tsCopy[len(tsCopy)-1]
		b.WriteString(fmt.Sprintf("--- Latest Metrics (at %s) ---\n", latest.Timestamp.Format(time.RFC3339Nano)))

		if len(latest.Servers) > 0 {
			b.WriteString("  Servers:\n")
			for id, sm := range latest.Servers {
				b.WriteString(fmt.Sprintf("    [%s] state=%s total_req=%d main_req=%d readyz_req=%d readyz_200=%d readyz_503=%d\n",
					id, sm.State,
					sm.Counters.TotalRequests, sm.Counters.MainRequests,
					sm.Counters.ReadyzRequests, sm.Counters.Readyz200, sm.Counters.Readyz503))
			}
		}

		if latest.Client != nil {
			c := latest.Client.Counters
			b.WriteString(fmt.Sprintf("  Client: sent=%d 2xx=%d 4xx=%d 5xx=%d errors=%d pre_readyz=%d\n",
				c.TotalSent, c.Status2xx, c.Status4xx, c.Status5xx, c.Errors, c.PreReadyz))
		}
	}

	// Latest TG snapshot
	if latestTG != nil {
		b.WriteString(fmt.Sprintf("\n--- Latest TG Snapshot (at %s) ---\n", latestTG.Timestamp.Format(time.RFC3339Nano)))
		b.WriteString(fmt.Sprintf("  healthy=%d unhealthy=%d initial=%d\n",
			latestTG.HealthyCount, latestTG.UnhealthyCount, latestTG.InitialCount))
		for target, state := range latestTG.Targets {
			b.WriteString(fmt.Sprintf("    %s -> %s\n", target, state))
		}
	}

	b.WriteString(fmt.Sprintf("\nTimeseries rows collected: %d\n", len(tsCopy)))
	b.WriteString("=== END REPORT ===\n")

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, b.String())
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}
