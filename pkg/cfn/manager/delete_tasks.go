package manager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/weaveworks/eksctl/pkg/cfn/outputs"
	"github.com/weaveworks/eksctl/pkg/spot"

	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/eks"
	"github.com/aws/aws-sdk-go/service/eks/eksiface"
	"github.com/pkg/errors"
	"github.com/weaveworks/eksctl/pkg/cfn/waiter"

	"github.com/kris-nova/logger"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	iamoidc "github.com/weaveworks/eksctl/pkg/iam/oidc"
	"github.com/weaveworks/eksctl/pkg/kubernetes"
	"github.com/weaveworks/eksctl/pkg/utils/tasks"
)

// think Jake is deleting this soon
func deleteAll(_ string) bool { return true }

// NewTasksToDeleteClusterWithNodeGroups defines tasks required to delete the given cluster along with all of its resources
func (c *StackCollection) NewTasksToDeleteClusterWithNodeGroups(deleteOIDCProvider bool, oidc *iamoidc.OpenIDConnectManager, clientSetGetter kubernetes.ClientSetGetter, wait bool, cleanup func(chan error, string) error) (*tasks.TaskTree, error) {
	taskTree := &tasks.TaskTree{Parallel: false}

	nodeGroupTasks, err := c.NewTasksToDeleteNodeGroups(deleteAll, true, cleanup)

	if err != nil {
		return nil, err
	}
	if nodeGroupTasks.Len() > 0 {
		nodeGroupTasks.IsSubTask = true
		taskTree.Append(nodeGroupTasks)
	}

	if deleteOIDCProvider {
		serviceAccountAndOIDCTasks, err := c.NewTasksToDeleteOIDCProviderWithIAMServiceAccounts(oidc, clientSetGetter)
		if err != nil {
			return nil, err
		}

		if serviceAccountAndOIDCTasks.Len() > 0 {
			serviceAccountAndOIDCTasks.IsSubTask = true
			taskTree.Append(serviceAccountAndOIDCTasks)
		}
	}

	deleteAddonIAMtasks, err := c.NewTaskToDeleteAddonIAM(wait)
	if err != nil {
		return nil, err
	}

	if deleteAddonIAMtasks.Len() > 0 {
		deleteAddonIAMtasks.IsSubTask = true
		taskTree.Append(deleteAddonIAMtasks)
	}

	clusterStack, err := c.DescribeClusterStack()
	if err != nil {
		return nil, err
	}
	if clusterStack == nil {
		return nil, &StackNotFoundErr{ClusterName: c.spec.Metadata.Name}
	}

	info := fmt.Sprintf("delete cluster control plane %q", c.spec.Metadata.Name)
	if wait {
		taskTree.Append(&taskWithStackSpec{
			info:  info,
			stack: clusterStack,
			call:  c.DeleteStackBySpecSync,
		})
	} else {
		taskTree.Append(&asyncTaskWithStackSpec{
			info:  info,
			stack: clusterStack,
			call:  c.DeleteStackBySpec,
		})
	}

	return taskTree, nil
}

// NewTasksToDeleteNodeGroups defines tasks required to delete all of the nodegroups
func (c *StackCollection) NewTasksToDeleteNodeGroups(shouldDelete func(string) bool, wait bool, cleanup func(chan error, string) error) (*tasks.TaskTree, error) {
	nodeGroupStacks, err := c.DescribeNodeGroupStacks()
	if err != nil {
		return nil, err
	}

	taskTree := &tasks.TaskTree{Parallel: false}

	// Nodegroups.
	{
		nodeGroupTaskTree := &tasks.TaskTree{Parallel: true}

		for _, s := range nodeGroupStacks {
			name := c.GetNodeGroupName(s)

			if !shouldDelete(name) || name == api.SpotOceanClusterNodeGroupName {
				continue
			}

			if *s.StackStatus == cloudformation.StackStatusDeleteFailed && cleanup != nil {
				nodeGroupTaskTree.Append(&tasks.TaskWithNameParam{
					Info: fmt.Sprintf("cleanup for nodegroup %q", name),
					Call: cleanup,
				})
			}
			info := fmt.Sprintf("delete nodegroup %q", name)
			if wait {
				nodeGroupTaskTree.Append(&taskWithStackSpec{
					info:  info,
					stack: s,
					call:  c.DeleteStackBySpecSync,
				})
			} else {
				nodeGroupTaskTree.Append(&asyncTaskWithStackSpec{
					info:  info,
					stack: s,
					call:  c.DeleteStackBySpec,
				})
			}
		}

		if nodeGroupTaskTree.Len() > 0 {
			nodeGroupTaskTree.IsSubTask = true
			taskTree.Append(nodeGroupTaskTree)
		}
	}

	// Spot Ocean.
	{
		oceanTaskTree, err := c.NewTasksToDeleteSpotOceanNodeGroup(shouldDelete)
		if err != nil {
			return nil, err
		}

		if oceanTaskTree.Len() > 0 {
			oceanTaskTree.IsSubTask = true
			taskTree.Append(oceanTaskTree)
		}
	}

	return taskTree, nil
}

type DeleteWaitCondition struct {
	Condition func() (bool, error)
	Timeout   time.Duration
	Interval  time.Duration
}

type DeleteUnownedNodegroupTask struct {
	cluster   string
	nodegroup string
	wait      *DeleteWaitCondition
	info      string
	eksAPI    eksiface.EKSAPI
}

func (d *DeleteUnownedNodegroupTask) Describe() string {
	return d.info
}

func (d *DeleteUnownedNodegroupTask) Do() error {
	out, err := d.eksAPI.DeleteNodegroup(&eks.DeleteNodegroupInput{
		ClusterName:   &d.cluster,
		NodegroupName: &d.nodegroup,
	})
	if err != nil {
		return err
	}

	if d.wait != nil {
		w := waiter.Waiter{
			NextDelay: func(_ int) time.Duration {
				return d.wait.Interval
			},
			Operation: d.wait.Condition,
		}

		if err := w.WaitWithTimeout(d.wait.Timeout); err != nil {
			if err == context.DeadlineExceeded {
				return errors.Errorf("timed out waiting for nodegroup deletion after %s", d.wait.Timeout)
			}
			return err
		}
	}

	if out != nil {
		logger.Debug("delete nodegroup %q output: %s", d.nodegroup, out.String())
	}
	return nil
}

func (c *StackCollection) NewTaskToDeleteUnownedNodeGroup(clusterName, nodegroup string, eksAPI eksiface.EKSAPI, waitCondition *DeleteWaitCondition) tasks.Task {
	return tasks.SynchronousTask{
		SynchronousTaskIface: &DeleteUnownedNodegroupTask{
			cluster:   clusterName,
			nodegroup: nodegroup,
			eksAPI:    eksAPI,
			wait:      waitCondition,
			info:      fmt.Sprintf("delete unowned nodegroup %s", nodegroup),
		}}
}

// NewTasksToDeleteOIDCProviderWithIAMServiceAccounts defines tasks required to delete all of the iamserviceaccounts
// along with associated IAM ODIC provider
func (c *StackCollection) NewTasksToDeleteOIDCProviderWithIAMServiceAccounts(oidc *iamoidc.OpenIDConnectManager, clientSetGetter kubernetes.ClientSetGetter) (*tasks.TaskTree, error) {
	taskTree := &tasks.TaskTree{Parallel: false}

	allServiceAccountsWithStacks, err := c.getAllServiceAccounts()
	if err != nil {
		return nil, err
	}
	saTasks, err := c.NewTasksToDeleteIAMServiceAccounts(allServiceAccountsWithStacks, clientSetGetter, true)
	if err != nil {
		return nil, err
	}

	if saTasks.Len() > 0 {
		saTasks.IsSubTask = true
		taskTree.Append(saTasks)
	}

	providerExists, err := oidc.CheckProviderExists()
	if err != nil {
		return nil, err
	}

	if providerExists {
		taskTree.Append(&asyncTaskWithoutParams{
			info: "delete IAM OIDC provider",
			call: oidc.DeleteProvider,
		})
	}

	return taskTree, nil
}

func (c *StackCollection) getAllServiceAccounts() ([]string, error) {
	serviceAccountStacks, err := c.DescribeIAMServiceAccountStacks()
	if err != nil {
		return nil, err
	}

	var serviceAccounts []string
	for _, stack := range serviceAccountStacks {
		serviceAccounts = append(serviceAccounts, GetIAMServiceAccountName(stack))
	}

	return serviceAccounts, nil
}

// NewTasksToDeleteIAMServiceAccounts defines tasks required to delete all of the iamserviceaccounts
func (c *StackCollection) NewTasksToDeleteIAMServiceAccounts(serviceAccounts []string, clientSetGetter kubernetes.ClientSetGetter, wait bool) (*tasks.TaskTree, error) {
	serviceAccountStacks, err := c.DescribeIAMServiceAccountStacks()
	if err != nil {
		return nil, err
	}

	stacksMap := stacksToServiceAccountMap(serviceAccountStacks)
	taskTree := &tasks.TaskTree{Parallel: true}

	for _, serviceAccount := range serviceAccounts {
		saTasks := &tasks.TaskTree{
			Parallel:  false,
			IsSubTask: true,
		}

		if s, ok := stacksMap[serviceAccount]; ok {
			info := fmt.Sprintf("delete IAM role for serviceaccount %q", serviceAccount)
			if wait {
				saTasks.Append(&taskWithStackSpec{
					info:  info,
					stack: s,
					call:  c.DeleteStackBySpecSync,
				})
			} else {
				saTasks.Append(&asyncTaskWithStackSpec{
					info:  info,
					stack: s,
					call:  c.DeleteStackBySpec,
				})
			}
		}

		meta, err := api.ClusterIAMServiceAccountNameStringToClusterIAMMeta(serviceAccount)
		if err != nil {
			return nil, err
		}
		saTasks.Append(&kubernetesTask{
			info:       fmt.Sprintf("delete serviceaccount %q", serviceAccount),
			kubernetes: clientSetGetter,
			objectMeta: meta.AsObjectMeta(),
			call:       kubernetes.MaybeDeleteServiceAccount,
		})
		taskTree.Append(saTasks)
	}

	return taskTree, nil
}

func stacksToServiceAccountMap(stacks []*cloudformation.Stack) map[string]*cloudformation.Stack {
	stackMap := make(map[string]*cloudformation.Stack)
	for _, stack := range stacks {
		stackMap[GetIAMServiceAccountName(stack)] = stack
	}

	return stackMap
}

// NewTaskToDeleteAddonIAM defines tasks required to delete all of the addons
func (c *StackCollection) NewTaskToDeleteAddonIAM(wait bool) (*tasks.TaskTree, error) {
	stacks, err := c.GetIAMAddonsStacks()
	if err != nil {
		return nil, err
	}
	taskTree := &tasks.TaskTree{Parallel: true}
	for _, s := range stacks {
		info := fmt.Sprintf("delete addon IAM %q", *s.StackName)

		deleteStackTasks := &tasks.TaskTree{
			Parallel:  false,
			IsSubTask: true,
		}
		if wait {
			deleteStackTasks.Append(&taskWithStackSpec{
				info:  info,
				stack: s,
				call:  c.DeleteStackBySpecSync,
			})
		} else {
			deleteStackTasks.Append(&asyncTaskWithStackSpec{
				info:  info,
				stack: s,
				call:  c.DeleteStackBySpec,
			})
		}
		taskTree.Append(deleteStackTasks)
	}
	return taskTree, nil

}

// NewTasksToDeleteSpotOceanNodeGroup defines tasks required to delete Ocean nodegroup.
func (c *StackCollection) NewTasksToDeleteSpotOceanNodeGroup(shouldDelete func(string) bool) (*tasks.TaskTree, error) {
	taskTree := &tasks.TaskTree{Parallel: true}

	// Check whether the Ocean Cluster's nodegroup should be deleted.
	stacks, err := c.DescribeNodeGroupStacks()
	if err != nil {
		return nil, err
	}
	stack, err := spot.ShouldDeleteOceanCluster(stacks, shouldDelete)
	if err != nil {
		return nil, err
	}
	if stack == nil { // nothing to do
		return taskTree, nil
	}

	// ignoreListImportsError ignores errors that may occur while listing imports.
	ignoreListImportsError := func(errMsg string) bool {
		errMsgs := []string{
			"not imported by any stack",
			"does not exist",
		}
		for _, msg := range errMsgs {
			if strings.Contains(strings.ToLower(errMsg), msg) {
				return true
			}
		}
		return false
	}

	// All nodegroups are marked for deletion. We need to wait for their deletion
	// to complete before deleting the Ocean Cluster.
	deleter := func(s *Stack, errs chan error) error {
		maxAttempts := 360 // 1 hour
		delay := 10 * time.Second

		for attempt := 1; ; attempt++ {
			logger.Debug("ocean: attempting to delete cluster (attempt: %d)", attempt)

			input := &cloudformation.ListImportsInput{
				ExportName: aws.String(fmt.Sprintf("%s::%s",
					aws.StringValue(s.StackName), outputs.NodeGroupSpotOceanClusterID)),
			}

			output, err := c.cloudformationAPI.ListImports(input)
			if err != nil {
				awsErr, ok := err.(awserr.Error)
				if !ok {
					return err
				}
				if !ignoreListImportsError(awsErr.Message()) {
					return err
				}
			}

			if output != nil && len(output.Imports) > 0 {
				if attempt+1 > maxAttempts {
					return fmt.Errorf("ocean: max attempts reached: " +
						"giving up waiting for importers to become deleted")
				}

				logger.Debug("ocean: waiting for %d importers "+
					"to become deleted", len(output.Imports))
				time.Sleep(delay)
				continue
			}

			logger.Debug("ocean: no more importers, deleting cluster...")
			return c.DeleteStackBySpecSync(s, errs)
		}
	}

	// Add a new deletion task.
	taskTree.Append(&taskWithStackSpec{
		info:  "delete ocean cluster",
		stack: stack,
		call:  deleter,
	})

	return taskTree, nil
}
