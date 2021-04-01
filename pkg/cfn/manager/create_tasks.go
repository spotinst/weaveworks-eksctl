package manager

import (
	"fmt"

	"github.com/kris-nova/logger"
	"github.com/pkg/errors"

	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	iamoidc "github.com/weaveworks/eksctl/pkg/iam/oidc"
	"github.com/weaveworks/eksctl/pkg/kubernetes"
	"github.com/weaveworks/eksctl/pkg/spot"
	"github.com/weaveworks/eksctl/pkg/utils/tasks"
	"github.com/weaveworks/eksctl/pkg/vpc"
)

const (
	managedByKubernetesLabelKey   = "app.kubernetes.io/managed-by"
	managedByKubernetesLabelValue = "eksctl"
)

// NewTasksToCreateClusterWithNodeGroups defines all tasks required to create a cluster along
// with some nodegroups; see CreateAllNodeGroups for how onlyNodeGroupSubset works
func (c *StackCollection) NewTasksToCreateClusterWithNodeGroups(nodeGroups []*api.NodeGroup,
	managedNodeGroups []*api.ManagedNodeGroup, supportsManagedNodes bool,
	postClusterCreationTasks ...tasks.Task) (*tasks.TaskTree, error) {

	taskTree := tasks.TaskTree{Parallel: false}

	// Control plane.
	{
		taskTree.Append(
			&createClusterTask{
				info:                 fmt.Sprintf("create cluster control plane %q", c.spec.Metadata.Name),
				stackCollection:      c,
				supportsManagedNodes: supportsManagedNodes,
			},
		)
	}

	// Nodegroups.
	{
		vpcImporter := vpc.NewStackConfigImporter(c.MakeClusterStackName())
		nodeGroupTaskTree, err := c.NewNodeGroupTask(nodeGroups, managedNodeGroups, supportsManagedNodes, false, vpcImporter)
		if err != nil {
			return nil, err
		}
		if nodeGroupTaskTree.Len() > 0 {
			nodeGroupTaskTree.IsSubTask = true
			taskTree.Append(nodeGroupTaskTree)
		}
	}

	// Post creation tasks.
	{
		if len(postClusterCreationTasks) > 0 {
			postTaskTree := &tasks.TaskTree{
				Parallel:  false,
				IsSubTask: true,
			}
			postTaskTree.Append(postClusterCreationTasks...)
			taskTree.Append(postTaskTree)
		}
	}

	return &taskTree, nil
}

// NewNodeGroupTask defines tasks required to create all of the nodegroups
func (c *StackCollection) NewNodeGroupTask(nodeGroups []*api.NodeGroup, managedNodeGroups []*api.ManagedNodeGroup,
	supportsManagedNodes bool, forceAddCNIPolicy bool, vpcImporter vpc.Importer) (*tasks.TaskTree, error) {
	taskTree := &tasks.TaskTree{Parallel: true}

	// Spot Ocean.
	{
		oceanTaskTree, err := c.NewSpotOceanNodeGroupTask(nodeGroups, vpcImporter)
		if err != nil {
			return nil, err
		}
		if oceanTaskTree.Len() > 0 {
			oceanTaskTree.IsSubTask = true
			taskTree.Parallel = false
			taskTree.Append(oceanTaskTree)
		}
	}

	// Unmanaged.
	{
		nodeGroupTaskTree := c.NewUnmanagedNodeGroupTask(nodeGroups, supportsManagedNodes, forceAddCNIPolicy, vpcImporter)
		if nodeGroupTaskTree.Len() > 0 {
			nodeGroupTaskTree.IsSubTask = true
			taskTree.Append(nodeGroupTaskTree)
		}
	}

	// Managed.
	{
		managedNodeGroupTaskTree := c.NewManagedNodeGroupTask(managedNodeGroups, forceAddCNIPolicy, vpcImporter)
		if managedNodeGroupTaskTree.Len() > 0 {
			managedNodeGroupTaskTree.IsSubTask = true
			taskTree.Append(managedNodeGroupTaskTree)
		}
	}

	return taskTree, nil
}

// NewUnmanagedNodeGroupTask defines tasks required to create unmanaged nodegroups
func (c *StackCollection) NewUnmanagedNodeGroupTask(nodeGroups []*api.NodeGroup,
	supportsManagedNodes bool, forceAddCNIPolicy bool, vpcImporter vpc.Importer) *tasks.TaskTree {

	taskTree := &tasks.TaskTree{Parallel: true}

	for _, ng := range nodeGroups {
		taskTree.Append(&nodeGroupTask{
			info:                 fmt.Sprintf("create nodegroup %q", ng.NameString()),
			nodeGroup:            ng,
			stackCollection:      c,
			supportsManagedNodes: supportsManagedNodes,
			forceAddCNIPolicy:    forceAddCNIPolicy,
			vpcImporter:          vpcImporter,
		})
		// TODO: move authconfigmap tasks here using kubernetesTask and kubernetes.CallbackClientSet
	}

	return taskTree
}

// NewManagedNodeGroupTask defines tasks required to create managed nodegroups
func (c *StackCollection) NewManagedNodeGroupTask(nodeGroups []*api.ManagedNodeGroup,
	forceAddCNIPolicy bool, vpcImporter vpc.Importer) *tasks.TaskTree {

	taskTree := &tasks.TaskTree{Parallel: true}
	for _, ng := range nodeGroups {
		taskTree.Append(&managedNodeGroupTask{
			stackCollection:   c,
			nodeGroup:         ng,
			forceAddCNIPolicy: forceAddCNIPolicy,
			vpcImporter:       vpcImporter,
			info:              fmt.Sprintf("create managed nodegroup %q", ng.Name),
		})
	}
	return taskTree
}

// NewSpotOceanNodeGroupTask defines tasks required to create Ocean nodegroup.
func (c *StackCollection) NewSpotOceanNodeGroupTask(nodeGroups []*api.NodeGroup,
	vpcImporter vpc.Importer) (*tasks.TaskTree, error) {

	taskTree := &tasks.TaskTree{Parallel: true}

	// Check whether the Ocean cluster's nodegroup should be created.
	stacks, err := c.DescribeNodeGroupStacks()
	if err != nil {
		return nil, err
	}
	ng := spot.ShouldCreateOceanNodeGroup(c.spec, nodeGroups, stacks)
	if ng == nil { // already exists OR --without-nodegroup
		return taskTree, nil
	}

	// Allow post-creation actions on this nodegroup.
	c.spec.NodeGroups = append(c.spec.NodeGroups, ng)

	// Add a new creation task.
	taskTree.Append(&nodeGroupTask{
		info:            "create ocean cluster",
		nodeGroup:       ng,
		stackCollection: c,
		vpcImporter:     vpcImporter,
	})

	return taskTree, nil
}

// NewClusterCompatTask creates a new task that checks for cluster compatibility with new features like
// Managed Nodegroups and Fargate, and updates the CloudFormation cluster stack if the required resources are missing
func (c *StackCollection) NewClusterCompatTask() tasks.Task {
	return &clusterCompatTask{
		stackCollection: c,
		info:            "fix cluster compatibility",
	}
}

// NewTasksToCreateIAMServiceAccounts defines tasks required to create all of the IAM ServiceAccounts
func (c *StackCollection) NewTasksToCreateIAMServiceAccounts(serviceAccounts []*api.ClusterIAMServiceAccount, oidc *iamoidc.OpenIDConnectManager, clientSetGetter kubernetes.ClientSetGetter) *tasks.TaskTree {
	taskTree := &tasks.TaskTree{Parallel: true}

	for i := range serviceAccounts {
		sa := serviceAccounts[i]
		saTasks := &tasks.TaskTree{
			Parallel:  false,
			IsSubTask: true,
		}

		if sa.AttachRoleARN == "" {
			saTasks.Append(&taskWithClusterIAMServiceAccountSpec{
				info:            fmt.Sprintf("create IAM role for serviceaccount %q", sa.NameString()),
				stackCollection: c,
				serviceAccount:  sa,
				oidc:            oidc,
			})
		} else {
			logger.Debug("attachRoleARN was provided, skipping role creation")
			sa.Status = &api.ClusterIAMServiceAccountStatus{
				RoleARN: &sa.AttachRoleARN,
			}
		}

		if !api.IsEnabled(sa.RoleOnly) {
			saTasks.Append(&kubernetesTask{
				info:       fmt.Sprintf("create serviceaccount %q", sa.NameString()),
				kubernetes: clientSetGetter,
				call: func(clientSet kubernetes.Interface) error {
					sa.SetAnnotations()
					if sa.Labels == nil {
						sa.Labels = make(map[string]string)
					}

					sa.Labels[managedByKubernetesLabelKey] = managedByKubernetesLabelValue
					if err := kubernetes.MaybeCreateServiceAccountOrUpdateMetadata(clientSet, sa.ClusterIAMMeta.AsObjectMeta()); err != nil {
						return errors.Wrapf(err, "failed to create service account %s", sa.NameString())
					}
					return nil
				},
			})
		}

		taskTree.Append(saTasks)
	}
	return taskTree
}
