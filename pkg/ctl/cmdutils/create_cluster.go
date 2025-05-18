package cmdutils

import (
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"io"
	"time"
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

	KopsClusterNameForVPC string
	Subnets               map[api.SubnetTopology]*[]string
	WithoutNodeGroup      bool
	Fargate               bool
	DryRun                bool
	EnableAutoMode        bool
	CreateNGOptions
	CreateManagedNGOptions
	CreateSpotOceanNodeGroupOptions

	ConfigReader io.Reader
}

// NodeGroupOptions holds options for creating nodegroups.
type NodeGroupOptions struct {
	CreateNGOptions
	CreateManagedNGOptions
	UpdateAuthConfigMap     *bool
	SkipOutdatedAddonsCheck bool
	SubnetIDs               []string
	SpotOcean               bool
}

// CreateManagedNGOptions holds options for creating a managed nodegroup
type CreateManagedNGOptions struct {
	Managed           bool
	Spot              bool
	NodeRepairEnabled bool
	InstanceTypes     []string
}

// CreateNGOptions holds options for creating a nodegroup
type CreateNGOptions struct {
	InstallNeuronDevicePlugin bool
	InstallNvidiaDevicePlugin bool
	DryRun                    bool
	NodeGroupParallelism      int
}

// CreateSpotOceanNodeGroupOptions holds options for creating a Spot Ocean nodegroup.
type CreateSpotOceanNodeGroupOptions struct {
	SpotOcean bool
}

// DeleteNodeGroupCmdParams groups CLI options for the delete nodegroup command.
type DeleteNodeGroupCmdParams struct {
	UpdateAuthConfigMap   bool
	Drain                 bool
	OnlyMissing           bool
	DisableEviction       bool
	DeleteNodeGroupDrain  bool
	Parallel              int
	MaxGracePeriod        time.Duration
	PodEvictionWaitPeriod time.Duration

	DeleteSpotOceanNodeGroupOptions
}

// DeleteSpotOceanNodeGroupOptions holds options for deleting a Spot Ocean nodegroup.
type DeleteSpotOceanNodeGroupOptions struct {
	SpotRoll          bool
	SpotRollBatchSize int
}
