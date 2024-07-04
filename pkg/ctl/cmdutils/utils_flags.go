package cmdutils

import (
	"github.com/spf13/pflag"
	"github.com/weaveworks/eksctl/pkg/spot/ocean"
)

func AddSpotOceanInstallOceanControllerFlags(fs *pflag.FlagSet, metricsServer *bool, namespace *string, releaseName *string) {
	fs.BoolVar(metricsServer, "metrics-server", false, "install an ocean controller with the metrics server")
	fs.StringVar(namespace, "namespace", ocean.DefaultNamespace, "install an ocean controller in the given namespace")
	fs.StringVar(releaseName, "release-name", ocean.DefaultReleaseName, "release name for the new ocean controller installation")
}
