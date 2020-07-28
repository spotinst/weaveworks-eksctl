package manager

import (
	"fmt"

	cfn "github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/pkg/errors"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	iamoidc "github.com/weaveworks/eksctl/pkg/iam/oidc"
	"github.com/weaveworks/eksctl/pkg/kubernetes"
	"github.com/weaveworks/eksctl/pkg/spot"
)

// NewTasksToCreateClusterWithNodeGroups defines all tasks required to create a cluster along
// with some nodegroups; see CreateAllNodeGroups for how onlyNodeGroupSubset works
func (c *StackCollection) NewTasksToCreateClusterWithNodeGroups(nodeGroups []*api.NodeGroup,
	managedNodeGroups []*api.ManagedNodeGroup, supportsManagedNodes bool,
	postClusterCreationTasks ...Task) (*TaskTree, error) {

	tasks := TaskTree{Parallel: false}

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

	subTasks := &TaskTree{
		Parallel:  false,
		IsSubTask: true,
	}

	// Post cluster creation.
	{
		if len(postClusterCreationTasks) > 0 {
			postTasks := &TaskTree{
				Parallel:  false,
				IsSubTask: true,
			}
			postTasks.Append(postClusterCreationTasks...)
			subTasks.Append(postTasks)
		}
	}

	// Nodegroups.
	{
		ngTasks, err := c.NewNodeGroupTask(nodeGroups, managedNodeGroups, supportsManagedNodes, false)
		if err != nil {
			return nil, err
		}
		if ngTasks.Len() > 0 {
			ngTasks.IsSubTask = true
			subTasks.Append(ngTasks)
		}
	}

	if subTasks.Len() > 0 {
		tasks.Append(subTasks)
	}

	return &tasks, nil
}

// NewNodeGroupTask defines tasks required to create all of the nodegroups
func (c *StackCollection) NewNodeGroupTask(nodeGroups []*api.NodeGroup,
	managedNodeGroups []*api.ManagedNodeGroup, supportsManagedNodes, forceAddCNIPolicy bool) (*TaskTree, error) {
	tasks := &TaskTree{Parallel: true}

	// Spot Ocean.
	oceanTasks, err := c.NewSpotOceanNodeGroupTask(nodeGroups)
	if err != nil {
		return nil, err
	}
	if oceanTasks.Len() > 0 {
		oceanTasks.IsSubTask = true
		tasks.Parallel = false
		tasks.Append(oceanTasks)
	}

	nodeGroupTasks := c.NewUnmanagedNodeGroupTask(nodeGroups, supportsManagedNodes, forceAddCNIPolicy)
	if nodeGroupTasks.Len() > 0 {
		if oceanTasks.Len() > 0 {
			nodeGroupTasks.IsSubTask = true
			tasks.Append(nodeGroupTasks)
		} else {
			tasks = nodeGroupTasks
		}
	}

	managedNodeGroupTasks := c.NewManagedNodeGroupTask(managedNodeGroups, forceAddCNIPolicy)
	if managedNodeGroupTasks.Len() > 0 {
		if oceanTasks.Len() > 0 {
			nodeGroupTasks.Append(managedNodeGroupTasks.tasks...)
		} else {
			tasks.Append(managedNodeGroupTasks.tasks...)
		}
	}

	return tasks, nil
}

// NewUnmanagedNodeGroupTask defines tasks required to create all of the nodegroups
func (c *StackCollection) NewUnmanagedNodeGroupTask(nodeGroups []*api.NodeGroup, supportsManagedNodes bool, forceAddCNIPolicy bool) *TaskTree {
	tasks := &TaskTree{Parallel: true}

	for _, ng := range nodeGroups {
		tasks.Append(&nodeGroupTask{
			info:                 fmt.Sprintf("create nodegroup %q", ng.NameString()),
			nodeGroup:            ng,
			stackCollection:      c,
			supportsManagedNodes: supportsManagedNodes,
			forceAddCNIPolicy:    forceAddCNIPolicy,
		})
		// TODO: move authconfigmap tasks here using kubernetesTask and kubernetes.CallbackClientSet
	}

	return tasks
}

// NewManagedNodeGroupTask defines tasks required to create managed nodegroups
func (c *StackCollection) NewManagedNodeGroupTask(nodeGroups []*api.ManagedNodeGroup, forceAddCNIPolicy bool) *TaskTree {
	tasks := &TaskTree{Parallel: true}

	for _, ng := range nodeGroups {
		tasks.Append(&managedNodeGroupTask{
			stackCollection:   c,
			nodeGroup:         ng,
			forceAddCNIPolicy: forceAddCNIPolicy,
			info:              fmt.Sprintf("create managed nodegroup %q", ng.Name),
		})
	}

	return tasks
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
				if err := kubernetes.MaybeCreateServiceAccountOrUpdateMetadata(clientSet, sa.ClusterIAMMeta.AsObjectMeta()); err != nil {
					return errors.Wrapf(err, "failed to create service account %s", sa.NameString())
				}
				return nil
			},
		})

		tasks.Append(saTasks)
	}
	return tasks
}

// NewSpotOceanNodeGroupTask defines tasks required to create Spot Ocean cluster.
func (c *StackCollection) NewSpotOceanNodeGroupTask(
	nodeGroups []*api.NodeGroup) (*TaskTree, error) {
	tasks := &TaskTree{Parallel: true}

	// Describe nodegroup stacks.
	stacks, err := c.DescribeNodeGroupStacks()
	if err != nil {
		// Do not fail if there are no eksctl-managed nodegroups.
		if err.Error() != c.errStackNotFound().Error() {
			return nil, err
		}

		stacks = []*cfn.Stack{}
	}

	// Verify before proceeding.
	ng, _, err := spot.ShouldCreateOceanNodeGroup(nodeGroups, stacks)
	if err != nil {
		return nil, err
	}
	if ng == nil { // already exists OR create without nodegroups
		return tasks, nil
	}

	// Allow post-creation actions to be performed on this nodegroup as well.
	c.spec.NodeGroups = append(c.spec.NodeGroups, ng)

	// Add a new creation task.
	tasks.Append(&nodeGroupTask{
		info:            "ocean: create cluster",
		nodeGroup:       ng,
		stackCollection: c,
	})

	return tasks, nil
}
