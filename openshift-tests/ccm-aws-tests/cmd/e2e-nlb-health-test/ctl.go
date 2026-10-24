package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// runCtl sends a control signal to the healthserver process running in the
// same pod (default PID 1). Used via kubectl exec so the signal reaches exactly
// one backend — never through the NLB.
//
// Commands:
//   readyz-false — SIGUSR1: readyz→503, keep serving (graceful shutdown start)
//   restart      — SIGUSR2: exit process; kubelet restarts the container
func runCtl(args []string) {
	pid := 1
	var command string
	for _, arg := range args {
		switch arg {
		case "readyz-false", "restart":
			command = arg
		default:
			if strings.HasPrefix(arg, "--pid=") {
				if n, err := strconv.Atoi(strings.TrimPrefix(arg, "--pid=")); err == nil && n > 0 {
					pid = n
				}
			}
		}
	}

	if command == "" {
		fmt.Fprintf(os.Stderr, "usage: e2e-nlb-health-test ctl [--pid=N] <readyz-false|restart>\n")
		os.Exit(1)
	}

	var sig syscall.Signal
	switch command {
	case "readyz-false":
		sig = syscall.SIGUSR1
	case "restart":
		sig = syscall.SIGUSR2
	default:
		fmt.Fprintf(os.Stderr, "ctl: unknown command %q (want readyz-false or restart)\n", command)
		os.Exit(1)
	}

	if err := syscall.Kill(pid, sig); err != nil {
		fmt.Fprintf(os.Stderr, "ctl: failed to send %v to pid %d: %v\n", sig, pid, err)
		os.Exit(1)
	}
	fmt.Printf("sent %v to pid %d\n", sig, pid)
}
