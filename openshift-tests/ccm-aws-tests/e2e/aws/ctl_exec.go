package aws

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/test/e2e/framework"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
)

const healthserverBinaryPath = "/e2e-nlb-health-test"

// execHealthserverCtl runs e2e-nlb-health-test ctl inside the named healthserver
// pod. Signals are delivered to PID 1 in the pod — never through the NLB.
func execHealthserverCtl(ctx context.Context, f *framework.Framework, namespace, podName, command string) error {
	stdout, stderr, err := e2epod.ExecCommandInContainerWithFullOutput(
		f, podName, "healthserver",
		healthserverBinaryPath, "ctl", command,
	)
	if err != nil {
		return fmt.Errorf("ctl %s on pod %s: %w (stdout=%q stderr=%q)", command, podName, err, stdout, stderr)
	}
	framework.Logf("[ctl] %s on pod %s: stdout=%q", command, podName, stdout)
	return nil
}

func getContainerRestartCount(ctx context.Context, f *framework.Framework, namespace, podName string) (int32, error) {
	pod, err := f.ClientSet.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return 0, err
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == "healthserver" {
			return cs.RestartCount, nil
		}
	}
	return 0, fmt.Errorf("healthserver container status not found in pod %s", podName)
}

// waitForContainerRestart waits until the healthserver container restart count
// increases after ctl restart (SIGUSR2 → process exit → kubelet restart).
func waitForContainerRestart(ctx context.Context, f *framework.Framework, namespace, podName string, baseline int32) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		count, err := getContainerRestartCount(ctx, f, namespace, podName)
		if err != nil {
			framework.Logf("[ctl] waiting for container restart: %v", err)
			return false, nil
		}
		framework.Logf("[ctl] container restart count: %d (baseline %d)", count, baseline)
		return count > baseline, nil
	})
}
