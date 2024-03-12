# Troubleshooting guide

## Common troubleshooting techniques

All Cloud Controller Manager (CCM) components are deployed in `openshift-cloud-controller-manager` namespace. You can check their status by executing `oc get all -n openshift-cloud-controller-manager`. To read CCM logs execute `oc logs -n openshift-cloud-controller-manager <platform>-cloud-controller-manager-<random suffix>`.

The operator that manages CCM is called Cluster Cloud Controller Manager Operator (CCCMO), and it is located in `openshift-cloud-controller-manager-operator` namespace. To check if it was deployed correctly, run `oc get all -n openshift-cloud-controller-manager-operator`.

## Cluster Cloud Controller Manager Operator is degraded

First, check the cluster operator status with `oc get clusteroperator cloud-controller-manager -oyaml` to find potential problems. Most likely the operator will have `Degraded` condition set to True.

Then check the main operator log `oc logs -n openshift-cloud-controller-manager-operator cluster-cloud-controller-manager-operator-<random suffix> -c cluster-cloud-controller-manager`.

Pay attention to the status of auxiliary controllers: Cloud Config Sync and Trusted CA Bundle Sync. Ensure that `CloudConfigControllerAvailable` and `TrustedCABundleControllerControllerAvailable` condition values are equal to True. If they are not, check their logs to find the reason: `oc logs -n openshift-cloud-controller-manager-operator cluster-cloud-controller-manager-operator-<random suffix> -c config-sync-controllers`.

## Migration from KCM to CCM got stuck

**Please note that KCM to CCM migration is only relevent for OpenShift version 4.14 and earlier.**

To troubleshoot the issue we need to understand how the migration works. When `ExternalCloudProvider` feature gate is set, Kube Controller Manager Operator updates all Kube Controller Manager (KCM) pods by setting `--cloud-provider external` there, and starts observing the pods statuses. If all of them started successfully, the operator sets condition `CloudControllerOwner: False` to the `kubecontrollermanager` resource. You can check it with `oc get kubecontrollermanager cluster -o yaml`.

In parallel with that CCCMO also monitors the `kubecontrollermanager` resource. It doesn't provision any Cloud Controller Manager related resources until the condition is set to False there.

When all KCM pods are restarted and KCM operator informs that it doesn't own cloud controllers anymore by setting the condition, CCCMO starts working. It deploys all necessary resources, monitors their statuses, and, if everything is fine, sets the same condition to True in its cluster operator resource. To checks this, execute `oc get clusteroperator cloud-controller-manager -oyaml` to see the value of `CloudControllerOwner` condition.

When CCCMO sets the condition, the migration is done. We expect this to take around 15 minutes.

Therefore, if you see that KCM->CCM migration got stuck:

1. Ensure that `CloudControllerOwner` condition is False on the `kubecontrollermanager` resource. If it's not, you may want to verify that all KCM pods have been restarted and they have `--cloud-contoller` set to `external`:

```sh
$ oc get pods -n openshift-kube-controller-manager-operator
$ oc get cm -n openshift-kube-controller-manager-operator cloud-config -o yaml
```

2. Check that the cluster operator resource for CCM has `CloudControllerOwner` set to True. The cause may be that the operator has some problems with deploying its resources. To verify it, look at `Degraded` condition. If it is equal to True, then you need to look at the operator logs to solve the issue.
