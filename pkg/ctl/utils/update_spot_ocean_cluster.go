package utils

import (
	"encoding/json"
	"fmt"

	"github.com/weaveworks/eksctl/pkg/nodebootstrap"

	"github.com/aws/amazon-ec2-instance-selector/v2/pkg/selector"
	"github.com/kris-nova/logger"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/cfn/builder"
	"github.com/weaveworks/eksctl/pkg/ctl/cmdutils"
	"github.com/weaveworks/eksctl/pkg/eks"
	"github.com/weaveworks/eksctl/pkg/printers"
	"github.com/weaveworks/eksctl/pkg/spot"
	"github.com/weaveworks/eksctl/pkg/vpc"
	"github.com/weaveworks/goformation/v4"
)

func updateSpotOceanCluster(cmd *cmdutils.Cmd) {
	cfg := api.NewClusterConfig()
	cmd.ClusterConfig = cfg

	cmd.SetDescription("update-spot-ocean-cluster", "Update Spot Ocean Cluster", "")

	cmd.CobraCommand.RunE = func(_ *cobra.Command, args []string) error {
		cmd.NameArg = cmdutils.GetNameArg(args)
		return doUpdateSpotOceanCluster(cmd)
	}

	cmd.FlagSetGroup.InFlagSet("General", func(fs *pflag.FlagSet) {
		cmdutils.AddConfigFileFlag(fs, &cmd.ClusterConfigFile)
		cmdutils.AddApproveFlag(fs, cmd)
		cmdutils.AddTimeoutFlag(fs, &cmd.ProviderConfig.WaitTimeout)
	})

	cmdutils.AddCommonFlagsForAWS(cmd.FlagSetGroup, &cmd.ProviderConfig, false)
}

func doUpdateSpotOceanCluster(cmd *cmdutils.Cmd) error {
	// Load configuration from file.
	// -------------------------------------------------------------------------

	if err := cmdutils.NewUtilsSpotOceanUpdateCluster(cmd).Load(); err != nil {
		return err
	}

	cfg := cmd.ClusterConfig
	meta := cmd.ClusterConfig.Metadata

	cmdutils.LogRegionAndVersionInfo(meta)

	if cfg.SpotOcean == nil {
		return nil
	}

	ng := spot.NewOceanClusterNodeGroup(cfg)

	ctl, err := cmd.NewCtl()
	if err != nil {
		return err
	}

	if ok, err := ctl.CanOperate(cfg); !ok {
		return err
	}

	if err := checkVersion(ctl, cfg.Metadata); err != nil {
		return err
	}

	stackManager := ctl.NewStackManager(cfg)

	if err := ctl.LoadClusterIntoSpecFromStack(cfg, stackManager); err != nil {
		return errors.Wrap(err, "couldn't load cluster into spec")
	}

	nodeGroupService := eks.NewNodeGroupService(ctl.Provider, selector.New(ctl.Provider.Session()))
	if err := nodeGroupService.Normalize([]api.NodePool{ng}, meta); err != nil {
		return err
	}

	printer := printers.NewJSONPrinter()
	if err := printer.LogObj(logger.Debug, "cfg.json = \\\n%s\n", cfg); err != nil {
		return err
	}

	stacks, err := stackManager.DescribeNodeGroupStacks()
	if err != nil {
		return err
	}

	stack := spot.GetStackByNodeGroupName(ng.Name, stacks)
	if stack == nil {
		return fmt.Errorf("ocean: couldn't find stack for nodegroup %q", ng.Name)
	}

	// Build a new stack from the default nodegroup.
	// -------------------------------------------------------------------------

	logger.Info("building nodegroup stack %q", ng.Name)
	clusterName := makeClusterStackName(cfg.Metadata.Name)
	vpcImporter := vpc.NewStackConfigImporter(clusterName)
	bootstrapper, err := nodebootstrap.NewBootstrapper(cfg, ng)
	if err != nil {
		return errors.Wrap(err, "error creating bootstrapper")
	}
	newStack := builder.NewNodeGroupResourceSet(ctl.Provider.EC2(), ctl.Provider.IAM(),
		cfg, ng, bootstrapper, stack.Tags, false, vpcImporter)
	if err := newStack.AddAllResources(); err != nil {
		return err
	}

	// Extract the NodeGroup resource from the new resource set.
	// -------------------------------------------------------------------------

	newTemplate := newStack.Template()
	newNodeGroup := newTemplate.Resources["NodeGroup"]
	newNodeGroupBytes, err := json.Marshal(newNodeGroup)
	if err != nil {
		return err
	}

	newNodeGroupCFN := struct{ Properties map[string]interface{} }{}
	if err = json.Unmarshal(newNodeGroupBytes, &newNodeGroupCFN); err != nil {
		return err
	}

	// Extract the NodeGroup resource from the existing resource set.
	// -------------------------------------------------------------------------

	existingTemplateBody, err := stackManager.GetStackTemplate(*stack.StackName)
	if err != nil {
		return err
	}

	existingTemplate, err := goformation.ParseJSON([]byte(existingTemplateBody))
	if err != nil {
		return fmt.Errorf("unexpected error parsing nodegroup template: %w", err)
	}

	existingNodeGroup, err := existingTemplate.GetCustomResourceWithName("NodeGroup")
	if err != nil {
		return fmt.Errorf("unable to find custom resource: %w", err)
	}

	// Override the resource properties of the Ocean Cluster.
	// -------------------------------------------------------------------------

	existingNodeGroup.Properties[api.SpotOceanClusterNodeGroupName] =
		newNodeGroupCFN.Properties[api.SpotOceanClusterNodeGroupName]

	// Update the stack.
	// -------------------------------------------------------------------------

	existingTemplateJSON, err := existingTemplate.JSON()
	if err != nil {
		return err
	}

	if !cmd.Plan {
		if err := stackManager.UpdateNodeGroupStack(ng.Name, string(existingTemplateJSON)); err != nil {
			return fmt.Errorf("error updating nodegroup stack: %w", err)
		}
	}

	cmdutils.LogPlanModeWarning(cmd.Plan)
	return nil
}

// makeClusterStackName generates the name of the cluster stack.
func makeClusterStackName(clusterName string) string {
	return "eksctl-" + clusterName + "-cluster"
}

// checkVersion configures the version based on control plane version.
func checkVersion(ctl *eks.ClusterProvider, meta *api.ClusterMeta) error {
	version := ctl.ControlPlaneVersion()
	if version == "" {
		return fmt.Errorf("unable to get control plane version")
	}

	meta.Version = version
	logger.Info("will use version %s for new nodegroup(s) based on "+
		"control plane version", meta.Version)

	return nil
}
