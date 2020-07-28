package manager

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/kris-nova/logger"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/cfn/outputs"
	iamoidc "github.com/weaveworks/eksctl/pkg/iam/oidc"
	"github.com/weaveworks/eksctl/pkg/kubernetes"
	"github.com/weaveworks/eksctl/pkg/spot"
	"github.com/weaveworks/eksctl/pkg/utils/tasks"
)

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
		return nil, c.ErrStackNotFound()
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

			if !shouldDelete(name) || name == api.SpotOceanNodeGroupName {
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

// NewTasksToDeleteOIDCProviderWithIAMServiceAccounts defines tasks required to delete all of the iamserviceaccounts
// along with associated IAM ODIC provider
func (c *StackCollection) NewTasksToDeleteOIDCProviderWithIAMServiceAccounts(oidc *iamoidc.OpenIDConnectManager, clientSetGetter kubernetes.ClientSetGetter) (*tasks.TaskTree, error) {
	taskTree := &tasks.TaskTree{Parallel: false}

	saTasks, err := c.NewTasksToDeleteIAMServiceAccounts(deleteAll, clientSetGetter, true)
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

// NewTasksToDeleteIAMServiceAccounts defines tasks required to delete all of the iamserviceaccounts
func (c *StackCollection) NewTasksToDeleteIAMServiceAccounts(shouldDelete func(string) bool, clientSetGetter kubernetes.ClientSetGetter, wait bool) (*tasks.TaskTree, error) {
	serviceAccountStacks, err := c.DescribeIAMServiceAccountStacks()
	if err != nil {
		return nil, err
	}

	taskTree := &tasks.TaskTree{Parallel: true}

	for _, s := range serviceAccountStacks {
		saTasks := &tasks.TaskTree{
			Parallel:  false,
			IsSubTask: true,
		}
		name := c.GetIAMServiceAccountName(s)

		if !shouldDelete(name) {
			continue
		}

		info := fmt.Sprintf("delete IAM role for serviceaccount %q", name)
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
		saTasks.Append(&kubernetesTask{
			info:       fmt.Sprintf("delete serviceaccount %q", name),
			kubernetes: clientSetGetter,
			call: func(clientSet kubernetes.Interface) error {
				meta, err := api.ClusterIAMServiceAccountNameStringToClusterIAMMeta(name)
				if err != nil {
					return err
				}
				return kubernetes.MaybeDeleteServiceAccount(clientSet, meta.AsObjectMeta())
			},
		})
		taskTree.Append(saTasks)
	}

	return taskTree, nil
}

// NewTasksToDeleteIAMServiceAccounts defines tasks required to delete all of the iamserviceaccounts
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

// NewTasksToDeleteSpotOceanNodeGroup defines tasks required to delete Spot
// Ocean cluster.
func (c *StackCollection) NewTasksToDeleteSpotOceanNodeGroup(shouldDelete func(string) bool) (*tasks.TaskTree, error) {
	taskTree := &tasks.TaskTree{Parallel: true}

	// Describe all nodegroup stacks.
	stacks, err := c.DescribeNodeGroupStacks()
	if err != nil {
		return nil, err
	}

	// Verify before proceeding.
	stack, err := spot.ShouldDeleteOceanNodeGroup(stacks, shouldDelete)
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

	// All nodegroups are marked for deletion. We have to wait for their deletion
	// to complete before proceeding for deletion of the Ocean cluster.
	deleter := func(s *Stack, errs chan error) error {
		maxAttempts := 360 // 1 hour
		delay := 10 * time.Second

		for attempt := 1; ; attempt++ {
			logger.Debug("ocean: attempting to delete cluster (attempt: %d)", attempt)

			input := &cloudformation.ListImportsInput{
				ExportName: aws.String(fmt.Sprintf("%s::%s",
					aws.StringValue(s.StackName), outputs.NodeGroupSpotOceanClusterID)),
			}

			output, err := c.provider.CloudFormation().ListImports(input)
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
