package utils

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spotinst/spotinst-sdk-go/spotinst/featureflag"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/ctl/cmdutils"
	"github.com/weaveworks/eksctl/pkg/ctl/cmdutils/filter"
	"github.com/weaveworks/eksctl/pkg/spot"
)

func updateSpotOceanCredentials(cmd *cmdutils.Cmd) {
	cfg := api.NewClusterConfig()
	ng := api.NewNodeGroup()
	cmd.ClusterConfig = cfg

	var spotProfile string

	cmd.SetDescription("update-spot-ocean-credentials", "Update Spot Ocean credentials", "")

	cmd.CobraCommand.RunE = func(_ *cobra.Command, args []string) error {
		cmd.NameArg = cmdutils.GetNameArg(args)
		return doUpdateSpotOceanCredentials(cmd, ng, spotProfile)
	}

	cmd.FlagSetGroup.InFlagSet("General", func(fs *pflag.FlagSet) {
		cmdutils.AddClusterFlag(fs, cfg.Metadata)
		cmdutils.AddRegionFlag(fs, cmd.ProviderConfig)
		cmdutils.AddConfigFileFlag(fs, &cmd.ClusterConfigFile)
		cmdutils.AddApproveFlag(fs, cmd)
		cmdutils.AddTimeoutFlag(fs, &cmd.ProviderConfig.WaitTimeout)
		fs.StringVarP(&ng.Name, "name", "n", "", "name of the nodegroup to update")
	})

	cmd.FlagSetGroup.InFlagSet("Spot", func(fs *pflag.FlagSet) {
		cmdutils.AddSpotOceanCommonFlags(fs, &spotProfile)
	})

	cmdutils.AddCommonFlagsForAWS(cmd.FlagSetGroup, cmd.ProviderConfig, false)
}

func doUpdateSpotOceanCredentials(cmd *cmdutils.Cmd, ng *api.NodeGroup, spotProfile string) error {
	ngFilter := filter.NewNodeGroupFilter()
	if err := cmdutils.NewUtilsSpotOceanUpdateCredentials(cmd, ng, ngFilter).Load(); err != nil {
		return err
	}

	cfg := cmd.ClusterConfig
	ctl, err := cmd.NewCtl()
	if err != nil {
		return err
	}
	if err := ctl.CheckAuth(); err != nil {
		return err
	}

	stackManager := ctl.NewStackManager(cfg)
	stacks, err := stackManager.DescribeNodeGroupStacks()
	if err != nil {
		return err
	}

	if cmd.ClusterConfigFile == "" {
		ng.SpotOcean = new(api.NodeGroupSpotOcean)
		cfg.NodeGroups = []*api.NodeGroup{ng}
	}

	for _, ng := range cfg.NodeGroups {
		if ng.SpotOcean != nil {
			cfg.NodeGroups = append(cfg.NodeGroups, &api.NodeGroup{
				Name:      api.SpotOceanNodeGroupName,
				SpotOcean: new(api.NodeGroupSpotOcean),
			})
			break
		}
	}

	logFiltered := cmdutils.ApplyFilter(cfg, ngFilter)
	logFiltered()

	featureflag.Set(fmt.Sprintf("%s=%t", spot.AllowCredentialsChanges.Name(), true))
	updateCredentials := func(ng *api.NodeGroup) error {
		if spotProfile != "" {
			ng.SpotOcean.Metadata = &api.NodeGroupSpotOceanMetadata{
				Profile: &spotProfile,
			}
		}
		return spot.UpdateCredentials(ctl.Provider, cfg, ng.Name, stacks)
	}

	for _, ng := range cfg.NodeGroups {
		if ng.SpotOcean != nil && !cmd.Plan {
			if err := updateCredentials(ng); err != nil {
				return err
			}
		}
	}

	cmdutils.LogPlanModeWarning(cmd.Plan)
	return nil
}
