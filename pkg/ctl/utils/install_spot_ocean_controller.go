package utils

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"github.com/weaveworks/eksctl/pkg/eks"
	"github.com/weaveworks/eksctl/pkg/kubernetes"
	"github.com/weaveworks/eksctl/pkg/spot/ocean"
	"github.com/weaveworks/eksctl/pkg/spot/ocean/providers/helm"
	"github.com/weaveworks/eksctl/pkg/utils/kubeconfig"
	"k8s.io/apimachinery/pkg/runtime"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/ctl/cmdutils"
)

type InstallSpotOceanControllerOptions struct {
	metricsServer bool
	namespace     string
	releaseName   string
}

func installSpotOceanController(cmd *cmdutils.Cmd) {
	cfg := api.NewClusterConfig()
	cmd.ClusterConfig = cfg

	var options InstallSpotOceanControllerOptions

	cmd.SetDescription("install-spot-ocean-controller", "Install Spot Ocean controller", "")

	cmd.CobraCommand.RunE = func(_ *cobra.Command, args []string) error {
		cmd.NameArg = cmdutils.GetNameArg(args)
		return doInstallSpotOceanController(cmd, options)
	}

	cmd.FlagSetGroup.InFlagSet("General", func(fs *pflag.FlagSet) {
		cmdutils.AddClusterFlagWithDeprecated(fs, cfg.Metadata)
		cmdutils.AddRegionFlag(fs, &cmd.ProviderConfig)
		cmdutils.AddConfigFileFlag(fs, &cmd.ClusterConfigFile)
		cmdutils.AddApproveFlag(fs, cmd)
		cmdutils.AddTimeoutFlag(fs, &cmd.ProviderConfig.WaitTimeout)
	})

	cmdutils.AddCommonFlagsForAWS(cmd, &cmd.ProviderConfig, false)

	cmd.FlagSetGroup.InFlagSet("Spot Ocean Controller", func(fs *pflag.FlagSet) {
		cmdutils.AddSpotOceanInstallOceanControllerFlags(fs, &options.metricsServer, &options.namespace, &options.releaseName)
	})
}

func doInstallSpotOceanController(cmd *cmdutils.Cmd, options InstallSpotOceanControllerOptions) error {
	if err := cmdutils.NewMetadataLoader(cmd).Load(); err != nil {
		return err
	}

	cfg := cmd.ClusterConfig

	ctx := context.TODO()
	ctl, err := cmd.NewProviderForExistingCluster(ctx)
	if err != nil {
		return err
	}

	if ok, err := ctl.CanUpdate(cfg); !ok {
		return err
	}

	config := kubeconfig.NewForKubectl(cfg, eks.GetUsername(ctl.Status.IAMRoleARN), "", cmd.ProviderConfig.Profile.Name)
	kubeConfigBytes, err := runtime.Encode(clientcmdlatest.Codec, config)
	if err != nil {
		return errors.Wrap(err, "generating kubeconfig")
	}

	restClientGetter := kubernetes.NewRESTClientGetter(options.namespace, string(kubeConfigBytes))

	helmInstaller, err := helm.NewInstaller(helm.Options{
		Namespace:        options.namespace,
		RESTClientGetter: restClientGetter,
	})
	if err != nil {
		return err
	}

	oceanInstaller := ocean.NewSpotOceanControllerInstaller(ocean.Options{
		HelmInstaller: helmInstaller,
		MetricsServer: options.metricsServer,
		Namespace:     options.namespace,
		ReleaseName:   options.releaseName,
		ClusterConfig: cfg,
	})

	if err := oceanInstaller.Install(context.Background()); err != nil {
		return fmt.Errorf("ocean: error installing controller: %w", err)
	}

	cmdutils.LogPlanModeWarning(cmd.Plan)
	return nil
}
