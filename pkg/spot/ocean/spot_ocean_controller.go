package ocean

import (
	"context"
	"fmt"
	"github.com/kris-nova/logger"
	"github.com/spotinst/spotinst-sdk-go/spotinst"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/spot/ocean/providers"
	"helm.sh/helm/v3/pkg/registry"
)

const (
	// DefaultNamespace default namespace for Spot Ocean Controller
	DefaultNamespace   = "spot-system"
	spotinstValue      = "spotinst"
	account            = "account"
	cluster            = "clusterIdentifier"
	token              = "token"
	repoURL            = "https://charts.spot.io"
	repoName           = "spot"
	helmChartName      = "ocean-kubernetes-controller"
	DefaultReleaseName = "ocean-controller"
	metricsServer      = "metrics-server"
	deployChart        = "deployChart"
)

// Options contains values which Spot Ocean Controller uses to configure the installation.
type Options struct {
	HelmInstaller providers.HelmInstaller
	ReleaseName   string
	Namespace     string
	MetricsServer bool
	ClusterConfig *api.ClusterConfig
}

// ChartInstaller defines a functionality to install Spot Ocean Controller.
//
//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate
//counterfeiter:generate -o fakes/fake_chart_installer.go . ChartInstaller
type ChartInstaller interface {
	Install(ctx context.Context, serviceAccountRoleARN string, instanceProfileName string) error
}

// Installer implements the Spot Ocean Controller installer functionality.
type Installer struct {
	Options
}

// NewSpotOceanControllerInstaller creates a new installer to configure and add Spot Ocean Controller to a cluster.
func NewSpotOceanControllerInstaller(opts Options) *Installer {
	return &Installer{
		Options: opts,
	}
}

// Install adds Spot Ocean Controller to a configured cluster in a separate CloudFormation stack.
func (o *Installer) Install(ctx context.Context) error {
	logger.Info("adding Spot Ocean Controller to cluster %s", o.ClusterConfig.Metadata.Name)
	logger.Info("cluster endpoint used by Spot Ocean Controller: %s", o.ClusterConfig.Status.Endpoint)

	config := spotinst.DefaultConfig()
	c, err := config.Credentials.Get()
	if err != nil {
		return err
	}

	values := map[string]interface{}{
		spotinstValue: map[string]interface{}{
			account: c.Account,
			cluster: o.ClusterConfig.Metadata.Name,
			token:   c.Token,
		},
		metricsServer: map[string]interface{}{
			deployChart: o.MetricsServer,
		},
	}

	registryClient, err := registry.NewClient(
		registry.ClientOptEnableCache(true),
	)
	if err != nil {
		return fmt.Errorf("failed to create registry client: %w", err)
	}

	options := providers.InstallChartOpts{
		RepoURL:         repoURL,
		RepoName:        repoName,
		ChartName:       helmChartName,
		CreateNamespace: true,
		Namespace:       o.Namespace,
		ReleaseName:     o.ReleaseName,
		Values:          values,
		RegistryClient:  registryClient,
	}

	logger.Info("the following chartOptions will be applied to the install: %+v", options)

	if err := o.HelmInstaller.InstallChart(ctx, options); err != nil {
		return fmt.Errorf("failed to install Spot Ocean Controller chart: %w", err)
	}
	return nil
}
