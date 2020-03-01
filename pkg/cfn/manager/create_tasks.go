package manager

import (
	"fmt"

	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	iamoidc "github.com/weaveworks/eksctl/pkg/iam/oidc"
	"github.com/weaveworks/eksctl/pkg/kubernetes"
)

// NewTasksToCreateClusterWithNodeGroups defines all tasks required to create a cluster along
// with some nodegroups; see CreateAllNodeGroups for how onlyNodeGroupSubset works
func (c *StackCollection) NewTasksToCreateClusterWithNodeGroups(nodeGroups []*api.NodeGroup,
	managedNodeGroups []*api.ManagedNodeGroup, supportsManagedNodes bool) (*TaskTree, error) {

	tasks := &TaskTree{Parallel: false}

	// Control plane.
	{
		tasks.Append(
			&createClusterTask{
				info:                 fmt.Sprintf("create cluster control plane %q", c.spec.Metadata.Name),
				stackCollection:      c,
				supportsManagedNodes: supportsManagedNodes,
			},
		)
	}

	// Nodegroups.
	{
		ngTasks, err := c.NewTasksToCreateNodeGroups(nodeGroups, managedNodeGroups, supportsManagedNodes)
		if err != nil {
			return nil, err
		}
		if ngTasks.Len() > 0 {
			ngTasks.IsSubTask = true
			tasks.Append(ngTasks)
		}
	}

	return tasks, nil
}

// NewTasksToCreateNodeGroups defines tasks required to create all of the nodegroups
func (c *StackCollection) NewTasksToCreateNodeGroups(nodeGroups []*api.NodeGroup,
	managedNodeGroups []*api.ManagedNodeGroup, supportsManagedNodes bool) (*TaskTree, error) {

	tasks := &TaskTree{Parallel: false}

	// Spot Ocean.
	{
		oceanTasks, err := c.NewTasksToCreateSpotOceanNodeGroup(nodeGroups, supportsManagedNodes)
		if err != nil {
			return nil, err
		}

		if oceanTasks.Len() > 0 {
			oceanTasks.IsSubTask = true
			tasks.Append(oceanTasks)
		}
	}

	// Nodegroups.
	{
		nodeGroupTasks, err := c.NewTasksToCreateUnmanagedNodeGroups(nodeGroups, supportsManagedNodes)
		if err != nil {
			return nil, err
		}

		managedNodeGroupTasks, err := c.NewTasksToCreateManagedNodeGroups(managedNodeGroups)
		if err != nil {
			return nil, err
		}

		if managedNodeGroupTasks.Len() > 0 {
			nodeGroupTasks.Append(managedNodeGroupTasks.tasks...)
		}

		if nodeGroupTasks.Len() > 0 {
			nodeGroupTasks.IsSubTask = true
			tasks.Append(nodeGroupTasks)
		}
	}

	return tasks, nil
}

// NewTasksToCreateUnmanagedNodeGroups defines tasks required to create all of the nodegroups
func (c *StackCollection) NewTasksToCreateUnmanagedNodeGroups(nodeGroups []*api.NodeGroup, supportsManagedNodes bool) (*TaskTree, error) {
	tasks := &TaskTree{Parallel: true}

	for _, ng := range nodeGroups {
		tasks.Append(&nodeGroupTask{
			info:                 fmt.Sprintf("create nodegroup %q", ng.NameString()),
			nodeGroup:            ng,
			stackCollection:      c,
			supportsManagedNodes: supportsManagedNodes,
		})
		// TODO: move authconfigmap tasks here using kubernetesTask and kubernetes.CallbackClientSet
	}

	return tasks, nil
}

// NewTasksToCreateManagedNodeGroups defines tasks required to create managed nodegroups
func (c *StackCollection) NewTasksToCreateManagedNodeGroups(nodeGroups []*api.ManagedNodeGroup) (*TaskTree, error) {
	tasks := &TaskTree{Parallel: true}

	for _, ng := range nodeGroups {
		tasks.Append(&managedNodeGroupTask{
			info:            fmt.Sprintf("create managed nodegroup %q", ng.Name),
			nodeGroup:       ng,
			stackCollection: c,
		})
	}

	return tasks, nil
}

// NewClusterCompatTask creates a new task that checks for cluster compatibility with new features like
// Managed Nodegroups and Fargate, and updates the CloudFormation cluster stack if the required resources are missing
func (c *StackCollection) NewClusterCompatTask() Task {
	return &clusterCompatTask{
		stackCollection: c,
		info:            "fix cluster compatibility",
	}
}

// NewTasksToCreateIAMServiceAccounts defines tasks required to create all of the IAM ServiceAccounts
func (c *StackCollection) NewTasksToCreateIAMServiceAccounts(serviceAccounts []*api.ClusterIAMServiceAccount, oidc *iamoidc.OpenIDConnectManager, clientSetGetter kubernetes.ClientSetGetter) *TaskTree {
	tasks := &TaskTree{Parallel: true}

	for i := range serviceAccounts {
		sa := serviceAccounts[i]
		saTasks := &TaskTree{
			Parallel:  false,
			IsSubTask: true,
		}

		saTasks.Append(&taskWithClusterIAMServiceAccountSpec{
			info:           fmt.Sprintf("create IAM role for serviceaccount %q", sa.NameString()),
			serviceAccount: sa,
			oidc:           oidc,
			call:           c.createIAMServiceAccountTask,
		})

		saTasks.Append(&kubernetesTask{
			info:       fmt.Sprintf("create serviceaccount %q", sa.NameString()),
			kubernetes: clientSetGetter,
			call: func(clientSet kubernetes.Interface) error {
				sa.SetAnnotations()
				return kubernetes.MaybeCreateServiceAccountOrUpdateMetadata(clientSet, sa.ObjectMeta)
			},
		})

		tasks.Append(saTasks)
	}
	return tasks
}
