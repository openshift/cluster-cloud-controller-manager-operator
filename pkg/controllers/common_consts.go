package controllers

const (
	DefaultManagedNamespace = "openshift-cloud-controller-manager"

	// OperatorNamespace is the namespace the operator's own Deployment (and the
	// resources it creates directly, e.g. the node-label-sync Job) run in.
	OperatorNamespace = "openshift-cloud-controller-manager-operator"

	infrastructureResourceName = "cluster"

	OpenshiftConfigNamespace        = "openshift-config"
	OpenshiftManagedConfigNamespace = "openshift-config-managed"

	syncedCloudConfigMapName = "cloud-conf"

	nodeLabelSyncJobName = "node-label-sync"

	proxyResourceName = "cluster"
)
