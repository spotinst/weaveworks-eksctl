package create

import (
	"fmt"

	"github.com/weaveworks/eksctl/pkg/spot"

	"github.com/kris-nova/logger"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	defaultaddons "github.com/weaveworks/eksctl/pkg/addons/default"
	"github.com/weaveworks/eksctl/pkg/cfn/manager"
	"github.com/weaveworks/eksctl/pkg/ctl/cmdutils/filter"
	"github.com/weaveworks/eksctl/pkg/eks"
	"github.com/weaveworks/eksctl/pkg/kubernetes"
	"github.com/weaveworks/eksctl/pkg/utils"
	"github.com/weaveworks/eksctl/pkg/utils/names"
	"github.com/weaveworks/eksctl/pkg/vpc"

	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/authconfigmap"
	"github.com/weaveworks/eksctl/pkg/ctl/cmdutils"
	"github.com/weaveworks/eksctl/pkg/printers"
)

func createNodeGroupCmd(cmd *cmdutils.Cmd) {
	createNodeGroupCmdWithRunFunc(cmd, func(cmd *cmdutils.Cmd, ng *api.NodeGroup, params *cmdutils.CreateNodeGroupCmdParams) error {
		return doCreateNodeGroups(cmd, ng, params)
	})
}

func createNodeGroupCmdWithRunFunc(cmd *cmdutils.Cmd, runFunc func(cmd *cmdutils.Cmd, ng *api.NodeGroup, params *cmdutils.CreateNodeGroupCmdParams) error) {
	cfg := api.NewClusterConfig()
	ng := api.NewNodeGroup()
	cmd.ClusterConfig = cfg

	params := &cmdutils.CreateNodeGroupCmdParams{}

	cfg.Metadata.Version = "auto"

	cmd.SetDescription("nodegroup", "Create a nodegroup", "", "ng")

	cmd.CobraCommand.RunE = func(_ *cobra.Command, args []string) error {
		cmd.NameArg = cmdutils.GetNameArg(args)
		return runFunc(cmd, ng, params)
	}

	exampleNodeGroupName := names.ForNodeGroup("", "")

	cmd.FlagSetGroup.InFlagSet("General", func(fs *pflag.FlagSet) {
		fs.StringVar(&cfg.Metadata.Name, "cluster", "", "name of the EKS cluster to add the nodegroup to")
		cmdutils.AddStringToStringVarPFlag(fs, &cfg.Metadata.Tags, "tags", "", map[string]string{}, "Used to tag the AWS resources")
		cmdutils.AddRegionFlag(fs, &cmd.ProviderConfig)
		cmdutils.AddVersionFlag(fs, cfg.Metadata, `for nodegroups "auto" and "latest" can be used to automatically inherit version from the control plane or force latest`)
		cmdutils.AddConfigFileFlag(fs, &cmd.ClusterConfigFile)
		cmdutils.AddNodeGroupFilterFlags(fs, &cmd.Include, &cmd.Exclude)
		cmdutils.AddUpdateAuthConfigMap(fs, &params.UpdateAuthConfigMap, "Add nodegroup IAM role to aws-auth configmap")
		cmdutils.AddTimeoutFlag(fs, &cmd.ProviderConfig.WaitTimeout)
	})

	cmd.FlagSetGroup.InFlagSet("New nodegroup", func(fs *pflag.FlagSet) {
		fs.StringVarP(&ng.Name, "name", "n", "", fmt.Sprintf("name of the new nodegroup (generated if unspecified, e.g. %q)", exampleNodeGroupName))
		cmdutils.AddCommonCreateNodeGroupFlags(fs, cmd, ng)
		fs.BoolVarP(&params.Managed, "managed", "", false, "Create EKS-managed nodegroup")
	})

	cmd.FlagSetGroup.InFlagSet("Spot", func(fs *pflag.FlagSet) {
		cmdutils.AddSpotOceanCommonFlags(fs, &params.SpotProfile)
		cmdutils.AddSpotOceanCreateNodeGroupFlags(fs, &params.SpotOcean)
	})

	cmd.FlagSetGroup.InFlagSet("Addons", func(fs *pflag.FlagSet) {
		cmdutils.AddCommonCreateNodeGroupIAMAddonsFlags(fs, ng)
		fs.BoolVarP(&params.InstallNeuronDevicePlugin, "install-neuron-plugin", "", true, "install Neuron plugin for Inferentia nodes")
	})

	cmdutils.AddCommonFlagsForAWS(cmd.FlagSetGroup, &cmd.ProviderConfig, true)
}

func doCreateNodeGroups(cmd *cmdutils.Cmd, ng *api.NodeGroup, params *cmdutils.CreateNodeGroupCmdParams) error {
	ngFilter := filter.NewNodeGroupFilter()

	if err := cmdutils.NewCreateNodeGroupLoader(cmd, ng, ngFilter, params.Managed, params.SpotOcean, params.SpotProfile).Load(); err != nil {
		return err
	}

	cfg := cmd.ClusterConfig
	meta := cmd.ClusterConfig.Metadata

	printer := printers.NewJSONPrinter()

	ctl, err := cmd.NewCtl()
	if err != nil {
		return err
	}
	cmdutils.LogRegionAndVersionInfo(meta)

	if err := ctl.CheckAuth(); err != nil {
		return err
	}

	if ok, err := ctl.CanOperate(cfg); !ok {
		return err
	}

	if err := checkVersion(cmd, ctl, cfg.Metadata); err != nil {
		return err
	}

	if err := ctl.LoadClusterIntoSpec(cfg); err != nil {
		return errors.Wrapf(err, "getting existing configuration for cluster %q", cfg.Metadata.Name)
	}

	stackManager := ctl.NewStackManager(cfg)

	if err := ngFilter.SetOnlyLocal(stackManager, cfg); err != nil {
		return err
	}

	logFiltered := cmdutils.ApplyFilter(cfg, ngFilter)

	clientSet, err := ctl.NewStdClientSet(cfg)
	if err != nil {
		return err
	}

	if err = checkARMSupport(ctl, clientSet, cfg); err != nil {
		return err
	}

	// EKS 1.14 clusters created with prior versions of eksctl may not support Managed Nodes
	supportsManagedNodes, err := ctl.SupportsManagedNodes(cfg)
	if err != nil {
		return err
	}

	if len(cfg.ManagedNodeGroups) > 0 && !supportsManagedNodes {
		return errors.New("Managed Nodegroups are not supported for this cluster version. Please update the cluster before adding managed nodegroups")
	}

	if err := eks.ValidateBottlerocketSupport(ctl.ControlPlaneVersion(), cmdutils.ToKubeNodeGroups(cfg)); err != nil {
		return err
	}

	nodeGroupService := eks.NewNodeGroupService(cfg, ctl.Provider)
	if err := nodeGroupService.Normalize(cmdutils.ToNodePools(cfg)); err != nil {
		return err
	}

	if err := printer.LogObj(logger.Debug, "cfg.json = \\\n%s\n", cfg); err != nil {
		return err
	}

	// TODO
	if err := ctl.ValidateClusterForCompatibility(cfg, stackManager); err != nil {
		return errors.Wrap(err, "cluster compatibility check failed")
	}

	if err := vpc.ValidateLegacySubnetsForNodeGroups(cfg, ctl.Provider); err != nil {
		return err
	}

	{
		logFiltered()
		logMsg := func(resource string, count int) {
			logger.Info("will create a CloudFormation stack for each of %d %s in cluster %q", count, resource, cfg.Metadata.Name)
		}
		if len(cfg.NodeGroups) > 0 {
			logMsg("nodegroups", len(cfg.NodeGroups))
		}

		if len(cfg.ManagedNodeGroups) > 0 {
			logMsg("managed nodegroups", len(cfg.ManagedNodeGroups))
		}

		tasks := &manager.TaskTree{
			Parallel: false,
		}
		if supportsManagedNodes {
			tasks.Append(stackManager.NewClusterCompatTask())
		}

		awsNodeUsesIRSA, err := eks.DoesAWSNodeUseIRSA(ctl.Provider, clientSet)
		if err != nil {
			return errors.Wrap(err, "couldn't check aws-node for annotation")
		}

		if !awsNodeUsesIRSA && api.IsEnabled(cfg.IAM.WithOIDC) {
			logger.Debug("cluster has withOIDC enabled but is not using IRSA for CNI, will add CNI policy to node role")
		}

		nodeGroupTasks, err := stackManager.NewNodeGroupTask(cfg.NodeGroups, cfg.ManagedNodeGroups, supportsManagedNodes, !awsNodeUsesIRSA)
		if err != nil {
			return fmt.Errorf("failed to create nodegroup tasks: %v", err)
		}

		tasks.Append(nodeGroupTasks)
		logger.Info(tasks.Describe())
		errs := tasks.DoAllSync()
		if len(errs) > 0 {
			logger.Info("%d error(s) occurred and nodegroups haven't been created properly, you may wish to check CloudFormation console", len(errs))
			logger.Info("to cleanup resources, run 'eksctl delete nodegroup --region=%s --cluster=%s --name=<name>' for each of the failed nodegroup", cfg.Metadata.Region, cfg.Metadata.Name)
			for _, err := range errs {
				if err != nil {
					logger.Critical("%s\n", err.Error())
				}
			}
			return fmt.Errorf("failed to create nodegroups for cluster %q", cfg.Metadata.Name)
		}
	}

	{ // post-creation action
		tasks := ctl.ClusterTasksForNodeGroups(cfg, params.InstallNeuronDevicePlugin)
		logger.Info(tasks.Describe())
		errs := tasks.DoAllSync()
		if len(errs) > 0 {
			logger.Info("%d error(s) occurred and nodegroups haven't been created properly, you may wish to check CloudFormation console", len(errs))
			logger.Info("to cleanup resources, run 'eksctl delete nodegroup --region=%s --cluster=%s --name=<name>' for each of the failed nodegroups", cfg.Metadata.Region, cfg.Metadata.Name)
			for _, err := range errs {
				if err != nil {
					logger.Critical("%s\n", err.Error())
				}
			}
			return fmt.Errorf("failed to create nodegroups for cluster %q", cfg.Metadata.Name)
		}

		// Spot Ocean.
		{
			// Initialize a new raw REST client.
			rawClient, err := ctl.NewRawClient(cfg)
			if err != nil {
				return err
			}

			// Execute post-creation actions.
			if err := spot.RunPostCreation(cfg, clientSet, rawClient,
				params.UpdateAuthConfigMap); err != nil {
				return err
			}
		}

		for _, ng := range cfg.NodeGroups {
			// skip Spot Ocean nodegroups
			if ng.SpotOcean != nil {
				continue
			}

			if params.UpdateAuthConfigMap {
				// authorise nodes to join
				if err = authconfigmap.AddNodeGroup(clientSet, ng); err != nil {
					return err
				}

				// wait for nodes to join
				if err = ctl.WaitForNodes(clientSet, ng); err != nil {
					return err
				}
			}

			showDevicePluginMessageForNodeGroup(ng, params.InstallNeuronDevicePlugin)
		}
		logger.Success("created %d nodegroup(s) in cluster %q", len(cfg.NodeGroups), cfg.Metadata.Name)

		for _, ng := range cfg.ManagedNodeGroups {
			if err := ctl.WaitForNodes(clientSet, ng); err != nil {
				if cfg.PrivateCluster.Enabled {
					logger.Info("error waiting for nodes to join the cluster; this command was likely run from outside the cluster's VPC as the API server is not reachable, nodegroup(s) should still be able to join the cluster, underlying error is: %v", err)
					break
				} else {
					return err
				}
			}
		}

		logger.Success("created %d managed nodegroup(s) in cluster %q", len(cfg.ManagedNodeGroups), cfg.Metadata.Name)
	}

	if err := ctl.ValidateExistingNodeGroupsForCompatibility(cfg, stackManager); err != nil {
		logger.Critical("failed checking nodegroups", err.Error())
	}

	return nil
}

func checkARMSupport(ctl *eks.ClusterProvider, clientSet kubernetes.Interface, cfg *api.ClusterConfig) error {
	rawClient, err := ctl.NewRawClient(cfg)
	if err != nil {
		return err
	}

	kubernetesVersion, err := rawClient.ServerVersion()
	if err != nil {
		return err
	}
	if api.ClusterHasInstanceType(cfg, utils.IsARMInstanceType) {
		upToDate, err := defaultaddons.DoAddonsSupportMultiArch(clientSet, rawClient, kubernetesVersion, ctl.Provider.Region())
		if err != nil {
			return err
		}
		if !upToDate {
			logger.Critical("to create an ARM nodegroup kube-proxy, coredns and aws-node addons should be up to date. " +
				"Please use `eksctl utils update-coredns`, `eksctl utils update-kube-proxy` and `eksctl utils update-aws-node` before proceeding.")
			return errors.New("expected default addons up to date")
		}
	}
	return nil
}
