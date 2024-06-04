package helm

import (
	"context"
	"fmt"
	"helm.sh/helm/v3/pkg/repo"
	"os"
	"time"

	"github.com/kris-nova/logger"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/weaveworks/eksctl/pkg/spot/ocean/providers"
)

// Options defines options for the Helm Installer.
type Options struct {
	Namespace        string
	RESTClientGetter genericclioptions.RESTClientGetter
}

// Installer implement the HelmInstaller interface.
type Installer struct {
	Settings     *cli.EnvSettings
	Getters      getter.Providers
	ActionConfig *action.Configuration
}

// NewInstaller creates a new Helm backed Installer for repo resources.
func NewInstaller(opts Options) (*Installer, error) {
	settings := cli.New()
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(opts.RESTClientGetter, opts.Namespace, "", logger.Debug); err != nil {
		return nil, fmt.Errorf("failed to initialize action config: %w", err)
	}
	return &Installer{
		Settings:     settings,
		Getters:      getter.All(settings),
		ActionConfig: actionConfig,
	}, nil
}

var _ providers.HelmInstaller = &Installer{}

// InstallChart takes a repo's name and a chart name and installs it. If namespace is not empty
// it will install into that namespace and create the namespace.
func (i *Installer) InstallChart(ctx context.Context, opts providers.InstallChartOpts) error {
	i.ActionConfig.RegistryClient = opts.RegistryClient

	client := action.NewInstall(i.ActionConfig)
	client.Wait = true
	client.Namespace = opts.Namespace
	client.ReleaseName = opts.ReleaseName
	client.CreateNamespace = opts.CreateNamespace
	client.Timeout = 10 * time.Minute

	logger.Info("updating repository %s", opts.RepoName)

	repoFile := i.Settings.RepositoryConfig
	if _, err := os.Stat(repoFile); os.IsNotExist(err) {
		f, err := os.Create(repoFile)
		if err != nil {
			return fmt.Errorf("failed to create repo file: %w", err)
		}
		err = f.Close()
		if err != nil {
			return fmt.Errorf("failed to close repo file: %w", err)
		}
	}

	file, err := repo.LoadFile(repoFile)
	if err != nil {
		return fmt.Errorf("failed to load repo file: %w", err)
	}

	var entry *repo.Entry
	for _, e := range file.Repositories {
		if e.Name == opts.RepoName {
			entry = e
			break
		}
	}

	if entry == nil {
		entry = &repo.Entry{
			Name: opts.RepoName,
			URL:  opts.RepoURL,
		}
		file.Repositories = append(file.Repositories, entry)
		err := file.WriteFile(repoFile, 0644)
		if err != nil {
			return err
		}
	}

	chartRepo, err := repo.NewChartRepository(entry, i.Getters)
	if err != nil {
		return fmt.Errorf("failed to create chart repository: %w", err)
	}

	_, err = chartRepo.DownloadIndexFile()
	if err != nil {
		return fmt.Errorf("failed to update repository: %w", err)
	}

	logger.Success("repository %s updated successfully", opts.RepoName)

	client.ChartPathOptions.RepoURL = opts.RepoURL

	chartPath, err := client.ChartPathOptions.LocateChart(opts.ChartName, i.Settings)
	if err != nil {
		return fmt.Errorf("failed to locate chart: %w", err)
	}

	// possibly deal with chart dependencies, but for now, maybe we don't care.
	ch, err := loader.Load(chartPath)
	if err != nil {
		return fmt.Errorf("failed to load chart: %w", err)
	}

	release, err := client.RunWithContext(ctx, ch, opts.Values)
	if err != nil {
		return fmt.Errorf("failed to install chart: %w", err)
	}

	logger.Success("successfully installed %s helm chart: %s/%s", release.Name, opts.ChartName, release.Version)

	return nil
}
