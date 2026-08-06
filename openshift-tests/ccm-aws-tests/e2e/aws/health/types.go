package health

import "time"

// HealthEvent records a target health state transition observed from the
// AWS DescribeTargetHealth API.
type HealthEvent struct {
	Timestamp  time.Time
	TargetID   string
	TargetPort int32
	State      string
	PrevState  string
	Reason     string
}

// RequestRecord captures a single HTTP request through the NLB, including
// connection-level timing from httptrace and server-reported state headers.
type RequestRecord struct {
	Timestamp       time.Time
	TargetIP        string
	TCPDialDuration time.Duration
	HTTPStatus      int
	ServerState     string
	ServerID        string
	FirstReadyzTime string
	IsNonReadyReq   bool
	Error           string
}
