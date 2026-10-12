package config

type TLSConsumer string

const (
	AWSCCM                TLSConsumer = "aws-ccm"
	AzureCCM              TLSConsumer = "azure-ccm"
	AzureStackCCM         TLSConsumer = "azurestack-ccm"
	GCPCCM                TLSConsumer = "gcp-ccm"
	IBMCCM                TLSConsumer = "ibm-ccm"
	NutanixCCM            TLSConsumer = "nutanix-ccm"
	OpenStackCCM          TLSConsumer = "openstack-ccm"
	PowerVSCCM            TLSConsumer = "powervs-ccm"
	VSphereCCM            TLSConsumer = "vsphere-ccm"
	AzureNodeManager      TLSConsumer = "azure-node-manager"
	AzureStackNodeManager TLSConsumer = "azurestack-node-manager"
)

func (cfg OperatorConfig) TLSCurvePreferencesFor(consumer TLSConsumer) string {
	switch consumer {
	case AWSCCM, AzureCCM, AzureStackCCM, GCPCCM, IBMCCM, OpenStackCCM, VSphereCCM, AzureNodeManager, AzureStackNodeManager:
		return cfg.TLSCurvePreferences
	default:
		return ""
	}
}
