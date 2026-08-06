package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type serverState string

const (
	statePreReadyz serverState = "pre-readyz"
	stateReady     serverState = "ready"
	stateDraining  serverState = "draining"
	stateShutdown  serverState = "shutdown"
)

type server struct {
	mu                sync.RWMutex
	id                string
	state             serverState
	processStart      time.Time
	tcpUp             time.Time
	firstReadyz200    *time.Time
	readyzFalseAt     *time.Time
	shutdownInitiated *time.Time
}

func main() {
	port := flag.Int("port", 8080, "Service port")
	startupDelay := flag.Duration("startup-delay", 30*time.Second, "Duration before /readyz returns 200")
	flag.Parse()

	id := os.Getenv("POD_NAME")
	if id == "" {
		id = fmt.Sprintf("healthserver-%d", os.Getpid())
	}

	s := &server{
		id:           id,
		state:        statePreReadyz,
		processStart: time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("POST /admin/readyz", s.handleAdminReadyz)
	mux.HandleFunc("POST /admin/shutdown", s.handleAdminShutdown)
	mux.HandleFunc("GET /admin/lifecycle", s.handleAdminLifecycle)
	mux.HandleFunc("/", s.handleMain)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: mux,
	}

	go func() {
		log.Printf("startup delay: %s (readyz returns 503 until then)", *startupDelay)
		time.Sleep(*startupDelay)
		s.mu.Lock()
		if s.state == statePreReadyz {
			now := time.Now()
			s.firstReadyz200 = &now
			s.state = stateReady
			log.Printf("startup delay elapsed, readyz now returns 200")
		}
		s.mu.Unlock()
	}()

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		sig := <-ch
		log.Printf("received %s, shutting down", sig)
		s.mu.Lock()
		now := time.Now()
		s.shutdownInitiated = &now
		s.state = stateShutdown
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	s.mu.Lock()
	s.tcpUp = time.Now()
	s.mu.Unlock()

	log.Printf("healthserver %s listening on :%d (startup-delay=%s)", id, *port, *startupDelay)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}

func (s *server) handleMain(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	state := s.state
	start := s.processStart
	var firstReadyz string
	if s.firstReadyz200 != nil {
		firstReadyz = s.firstReadyz200.Format(time.RFC3339Nano)
	} else {
		firstReadyz = "never"
	}
	s.mu.RUnlock()

	w.Header().Set("X-Server-State", string(state))
	w.Header().Set("X-Server-ID", s.id)
	w.Header().Set("X-Server-Start-Time", start.Format(time.RFC3339Nano))
	w.Header().Set("X-First-Readyz-Time", firstReadyz)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "server_id=%s state=%s\n", s.id, state)
}

func (s *server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	state := s.state
	s.mu.RUnlock()

	if state == stateReady {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, "not ready: %s", state)
	}
}

func (s *server) handleAdminReadyz(w http.ResponseWriter, r *http.Request) {
	ready := r.URL.Query().Get("ready")
	s.mu.Lock()
	defer s.mu.Unlock()

	switch ready {
	case "true":
		if s.firstReadyz200 == nil {
			now := time.Now()
			s.firstReadyz200 = &now
		}
		s.state = stateReady
		fmt.Fprint(w, "readyz=true")
	case "false":
		now := time.Now()
		s.readyzFalseAt = &now
		s.state = stateDraining
		fmt.Fprint(w, "readyz=false")
	default:
		http.Error(w, "ready must be 'true' or 'false'", http.StatusBadRequest)
	}
}

func (s *server) handleAdminShutdown(w http.ResponseWriter, r *http.Request) {
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

	s.mu.Lock()
	now := time.Now()
	s.shutdownInitiated = &now
	s.state = stateShutdown
	s.mu.Unlock()

	fmt.Fprintf(w, "shutdown initiated, delay=%s", delay)

	go func() {
		time.Sleep(delay)
		os.Exit(0)
	}()
}

func (s *server) handleAdminLifecycle(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	j := struct {
		ServerID          string  `json:"server_id"`
		ProcessStart      string  `json:"t_process_start"`
		TCPUp             string  `json:"t_tcp_up"`
		FirstReadyz200    *string `json:"t_first_readyz_200"`
		ReadyzFalseAt     *string `json:"t_readyz_false_at"`
		ShutdownInitiated *string `json:"t_shutdown_initiated"`
	}{
		ServerID:     s.id,
		ProcessStart: s.processStart.Format(time.RFC3339Nano),
		TCPUp:        s.tcpUp.Format(time.RFC3339Nano),
	}
	if s.firstReadyz200 != nil {
		v := s.firstReadyz200.Format(time.RFC3339Nano)
		j.FirstReadyz200 = &v
	}
	if s.readyzFalseAt != nil {
		v := s.readyzFalseAt.Format(time.RFC3339Nano)
		j.ReadyzFalseAt = &v
	}
	if s.shutdownInitiated != nil {
		v := s.shutdownInitiated.Format(time.RFC3339Nano)
		j.ShutdownInitiated = &v
	}
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(j)
}
