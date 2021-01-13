package cmdutils

import (
	"time"

	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
)

// CreateClusterCmdParams groups CLI options for the create cluster command.
type CreateClusterCmdParams struct {
	WriteKubeconfig             bool
	KubeconfigPath              string
	AutoKubeconfigPath          bool
	AuthenticatorRoleARN        string
	SetContext                  bool
	AvailabilityZones           []string
	InstallWindowsVPCController bool
	InstallNeuronDevicePlugin   bool
	InstallNvidiaDevicePlugin   bool
	KopsClusterNameForVPC       string
	Subnets                     map[api.SubnetTopology]*[]string
	WithoutNodeGroup            bool
	Fargate                     bool

	CreateManagedNGOptions
	CreateSpotOceanNodeGroupOptions
}

// CreateManagedNGOptions holds options for creating a managed nodegroup
type CreateManagedNGOptions struct {
	Managed       bool
	Spot          bool
	InstanceTypes []string
}

// CreateSpotOceanNodeGroupOptions holds options for creating a Spot Ocean nodegroup.
type CreateSpotOceanNodeGroupOptions struct {
	SpotOcean   bool
	SpotProfile string
}

// DeleteNodeGroupCmdParams groups CLI options for the delete nodegroup command.
type DeleteNodeGroupCmdParams struct {
	UpdateAuthConfigMap bool
	Drain               bool
	OnlyMissing         bool
	MaxGracePeriod      time.Duration

	DeleteSpotOceanNodeGroupOptions
}

// DeleteSpotOceanNodeGroupOptions holds options for deleting a Spot Ocean nodegroup.
type DeleteSpotOceanNodeGroupOptions struct {
	SpotProfile       string
	SpotRoll          bool
	SpotRollBatchSize int
}
