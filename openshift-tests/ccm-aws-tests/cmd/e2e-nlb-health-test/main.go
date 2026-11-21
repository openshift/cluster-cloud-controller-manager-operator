package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "client":
		runClient(os.Args[2:])
	case "aggregator":
		runAggregator(os.Args[2:])
	case "ctl":
		runCtl(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: e2e-nlb-health-test <subcommand> [flags]

Subcommands:
  serve       Health-controllable HTTP server (runs on control-plane nodes)
  client      HTTP request generator (runs on worker nodes)
  aggregator  Metrics aggregator and report generator (runs on worker node)
  ctl         Send control signal to local serve process (via kubectl exec)

Examples:
  e2e-nlb-health-test serve      --port=19443 --startup-delay=60s --aggregator=http://agg:8090
  e2e-nlb-health-test client     --url=http://NLB:19443/ --workers=8 --aggregator=http://agg:8090
  e2e-nlb-health-test aggregator --port=8090 --scrape-interval=1s
  e2e-nlb-health-test ctl readyz-false
  e2e-nlb-health-test ctl restart
`)
}
