package main

import "time"

// Event is a lifecycle event pushed by agents to the aggregator.
// Events capture exact timestamps for one-time state changes
// (SIGTERM, readyz transitions, pre-readyz detection) that may
// fall between scrape intervals.
type Event struct {
	Source   string    `json:"source"`              // "server", "client", "tg"
	ServerID string   `json:"server_id,omitempty"`  // pod name or instance ID
	Event    string   `json:"event"`               // see event constants below
	Detail   string   `json:"detail,omitempty"`    // extra context
	Timestamp time.Time `json:"timestamp"`
}

// Event name constants
const (
	EventSigterm       = "sigterm"
	EventReadyzFalse   = "readyz_false"
	EventReadyzTrue    = "readyz_true"
	EventTCPUp         = "tcp_up"
	EventPreReadyz     = "pre_readyz_detected"
	EventRegistered    = "agent_registered"
)

// ServerMetrics is returned by GET /metrics on the serve subcommand.
// Scraped by the aggregator every N seconds.
type ServerMetrics struct {
	ServerID  string         `json:"server_id"`
	Node      string         `json:"node,omitempty"`
	State     string         `json:"state"`
	Timestamp time.Time      `json:"timestamp"`
	Lifecycle Lifecycle      `json:"lifecycle"`
	Counters  ServerCounters `json:"counters"`
}

// Lifecycle holds server lifecycle timestamps.
type Lifecycle struct {
	ProcessStart   *time.Time `json:"t_process_start,omitempty"`
	TCPUp          *time.Time `json:"t_tcp_up,omitempty"`
	FirstReadyz200 *time.Time `json:"t_first_readyz_200,omitempty"`
	ReadyzFalseAt  *time.Time `json:"t_readyz_false_at,omitempty"`
	Sigterm        *time.Time `json:"t_sigterm,omitempty"`
}

// ServerCounters tracks request counts by endpoint. readyz_requests
// counts NLB HC probes (only the HC agent calls /readyz).
// main_requests counts client traffic (/).
type ServerCounters struct {
	TotalRequests  int64 `json:"total_requests"`
	MainRequests   int64 `json:"main_requests"`
	ReadyzRequests int64 `json:"readyz_requests"`
	Readyz200      int64 `json:"readyz_200"`
	Readyz503      int64 `json:"readyz_503"`
}

// ClientMetrics is returned by GET /metrics on the client subcommand.
// Scraped by the aggregator every N seconds.
type ClientMetrics struct {
	Timestamp time.Time                `json:"timestamp"`
	Counters  ClientCounters           `json:"counters"`
	PerServer map[string]PerServerCount `json:"per_server"`
}

// ClientCounters tracks overall client send statistics.
type ClientCounters struct {
	TotalSent int64 `json:"total_sent"`
	Status2xx int64 `json:"status_2xx"`
	Status4xx int64 `json:"status_4xx"`
	Status5xx int64 `json:"status_5xx"`
	Errors    int64 `json:"errors"`
	PreReadyz int64 `json:"pre_readyz"`
}

// PerServerCount tracks how many requests went to a specific server.
type PerServerCount struct {
	Total     int64 `json:"total"`
	PreReadyz int64 `json:"pre_readyz"`
}

// ClientRecord captures a single HTTP request (sent by client, stored for
// full-detail retrieval via GET /records).
type ClientRecord struct {
	Timestamp       time.Time     `json:"timestamp"`
	TargetIP        string        `json:"target_ip"`
	TCPDialDuration time.Duration `json:"tcp_dial_ms"`
	HTTPStatus      int           `json:"http_status"`
	ServerState     string        `json:"server_state"`
	ServerID        string        `json:"server_id"`
	ServerStartTime string        `json:"server_start_time"`
	FirstReadyzTime string        `json:"first_readyz_time"`
	IsNonReadyReq   bool          `json:"is_non_ready_req"`
	Error           string        `json:"error,omitempty"`
}

// TGSnapshot is a target group health snapshot pushed by the test binary.
type TGSnapshot struct {
	Timestamp      time.Time         `json:"timestamp"`
	Targets        map[string]string `json:"targets"`
	HealthyCount   int               `json:"healthy_count"`
	UnhealthyCount int               `json:"unhealthy_count"`
	InitialCount   int               `json:"initial_count"`
}

// TimeseriesRow is one row of the consolidated time-series, combining
// all three data sources at a single point in time.
type TimeseriesRow struct {
	Timestamp time.Time                 `json:"timestamp"`
	Servers   map[string]ServerMetrics  `json:"servers,omitempty"`
	Client    *ClientMetrics            `json:"client,omitempty"`
	TG        *TGSnapshot               `json:"tg,omitempty"`
}
