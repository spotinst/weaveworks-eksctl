package spot

import (
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/kris-nova/logger"
	"github.com/weaveworks/eksctl/pkg/addons"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/authconfigmap"
	"github.com/weaveworks/eksctl/pkg/cfn/outputs"
	"github.com/weaveworks/eksctl/pkg/kubernetes"
	kubewrapper "github.com/weaveworks/eksctl/pkg/kubernetes"
)

// ErrSpotMultipleDefaultLaunchSpecs represents an error in detecting the default
// nodegroup since more than one has been configured as such.
var ErrSpotMultipleDefaultLaunchSpecs = errors.New("spot: unable to detect " +
	"default ocean launch spec: multiple nodegroups configured with " +
	"`spot.metadata.defaultLaunchSpec: true`")

// RunPreCreation executes pre-creation actions.
func RunPreCreation(clusterConfig *api.ClusterConfig, stacks []*cloudformation.Stack) error {
	logger.Debug("spot: executing pre-creation actions")

	for _, ng := range clusterConfig.NodeGroups {
		if ng.SpotOcean == nil || (ng.SpotOcean.Metadata != nil &&
			ng.SpotOcean.Metadata.ClusterID != nil) {
			continue
		}

		if ng.SpotOcean.Metadata == nil {
			ng.SpotOcean.Metadata = new(api.NodeGroupSpotOceanMetadata)
		}

		if len(stacks) > 0 {
			logger.Debug("spot: collecting ocean cluster identifier for nodegroup %q", ng.Name)
			if err := ensureNodeGroupOceanClusterID(stacks, ng); err != nil {
				return err
			}
		}
	}

	return nil
}

// RunPreCreation executes post-creation actions.
func RunPostCreation(clusterConfig *api.ClusterConfig, clientSet kubernetes.Interface,
	rawClient *kubewrapper.RawClient, updateAuthConfigMap, planMode bool) error {

	logger.Debug("spot: executing post-creation actions")
	for _, ng := range clusterConfig.NodeGroups {
		if ng.SpotOcean == nil {
			continue
		}

		// Authorise Ocean nodes to join. We have to do it before all other
		// nodegroups to prevent `WaitForNodes` to wait forever.
		if updateAuthConfigMap {
			if err := authconfigmap.AddNodeGroup(clientSet, ng); err != nil {
				return fmt.Errorf("spot: error updaing auth configmap: %w", err)
			}
		}

		// Install the Ocean controller.
		if ng.Name == api.SpotOceanNodeGroupName {
			controller := addons.NewSpotOceanController(
				rawClient,
				clusterConfig,
				planMode,
				aws.StringValue(ng.SpotOcean.Metadata.Profile))
			if err := controller.Deploy(); err != nil {
				return fmt.Errorf("spot: error installing ocean controller: %w", err)
			}
		}
	}

	return nil
}

// RunPreDeletion executes pre-deletion actions.
func RunPreDeletion(clusterConfig *api.ClusterConfig, stacks []*cloudformation.Stack) error {
	logger.Debug("spot: executing pre-deletion actions")

	shouldDelete := func(ngName string) bool {
		for _, ng := range clusterConfig.NodeGroups {
			if ng.Name == ngName {
				return true
			}
		}
		return false
	}

	s, err := ShouldDeleteOceanNodeGroup(stacks, shouldDelete)
	if err != nil {
		return err
	}

	if s != nil {
		// Allow post-deletion actions to be performed on Ocean as well.
		clusterConfig.NodeGroups = append(clusterConfig.NodeGroups, &api.NodeGroup{
			Name: api.SpotOceanNodeGroupName,
		})
	}

	return nil
}

// ShouldCreateOceanNodeGroup checks whether the nodegroup of the Ocean cluster
// should be created and, if so, returns its NodeGroup configuration.
func ShouldCreateOceanNodeGroup(nodeGroups []*api.NodeGroup) (*api.NodeGroup, error) {
	logger.Debug("spot: checking whether ocean cluster should be created")

	var (
		oceanNodeGroups           = make([]*api.NodeGroup, 0, len(nodeGroups))
		oceanNodeGroup            *api.NodeGroup
		desired, minimum, maximum int
	)

	// If there are no Ocean nodegroups, let's bail early.
	for _, ng := range nodeGroups {
		if ng.SpotOcean != nil {
			oceanNodeGroups = append(oceanNodeGroups, ng)
		}
	}
	if len(oceanNodeGroups) == 0 {
		return nil, nil
	}

	// Find the default nodegroup and calculate the capacity.
	for _, ng := range oceanNodeGroups {
		// Is this the default nodegroup?
		if aws.BoolValue(ng.SpotOcean.Metadata.DefaultLaunchSpec) {
			if oceanNodeGroup != nil {
				logger.Debug("spot: multiple default nodegroups (%q and %q)",
					ng.Name, oceanNodeGroup.Name)
				return nil, ErrSpotMultipleDefaultLaunchSpecs
			}
			oceanNodeGroup = ng.DeepCopy()
		}

		// Sum up the capacity from all nodegroups.
		desired += aws.IntValue(ng.DesiredCapacity)
		minimum += aws.IntValue(ng.MinSize)
		maximum += aws.IntValue(ng.MaxSize)
	}

	// No default nodegroup. Take the first one.
	if oceanNodeGroup == nil {
		oceanNodeGroup = oceanNodeGroups[0].DeepCopy()
	}
	logger.Debug("spot: using default nodegroup %q", oceanNodeGroup.Name)

	// Set the capacity.
	oceanNodeGroup.DesiredCapacity = aws.Int(desired)
	oceanNodeGroup.MinSize = aws.Int(minimum)
	oceanNodeGroup.MaxSize = aws.Int(maximum)

	// If there is already an existing cluster, we're done.
	if oceanNodeGroup.SpotOcean.Metadata.ClusterID != nil {
		logger.Debug("spot: ocean cluster already exists")
		return nil, nil
	}

	// Configure the nodegroup name.
	oceanNodeGroup.Name = api.SpotOceanNodeGroupName

	return oceanNodeGroup, nil
}

// ShouldDeleteOceanNodeGroup checks whether the nodegroup of the Ocean cluster
// should be deleted and, if so, returns its Cloud Formation stack.
func ShouldDeleteOceanNodeGroup(stacks []*cloudformation.Stack,
	shouldDelete func(string) bool) (*cloudformation.Stack, error) {

	logger.Debug("spot: checking whether ocean cluster should be deleted")
	var oceanNodeGroupStack *cloudformation.Stack

	// If there is no Ocean cluster nodegroup, let's bail early.
	for _, s := range stacks {
		if nodeGroupName(s) == api.SpotOceanNodeGroupName {
			oceanNodeGroupStack = s
			break
		}
	}
	if oceanNodeGroupStack == nil {
		logger.Debug("spot: ocean cluster does not exist; nothing to delete")
		return nil, nil
	}

	// Do not delete if there is at least one nodegroup that is not marked for deletion.
	for _, s := range stacks {
		name := nodeGroupName(s)

		if !shouldDelete(name) &&
			name != api.SpotOceanNodeGroupName &&
			nodeGroupStatusIsNotTransitional(s) {

			for _, tag := range s.Tags {
				if aws.StringValue(tag.Key) == api.SpotOceanResourceTypeTag {
					logger.Debug("spot: at least one nodegroup remains "+
						"active (%s); skipping ocean cluster deletion", name)
					return nil, nil
				}
			}
		}
	}

	// All nodegroups are marked for deletion.
	return oceanNodeGroupStack, nil
}

// ensureNodeGroupOceanClusterID retrieves the Ocean cluster identifier.
func ensureNodeGroupOceanClusterID(stacks []*cloudformation.Stack, nodeGroup *api.NodeGroup) error {
	for _, s := range stacks {
		if nodeGroupName(s) == api.SpotOceanNodeGroupName {
			if !nodeGroupStatusIsNotTransitional(s) {
				return fmt.Errorf("spot: nodegroup %q is in transitional state %q",
					nodeGroup.Name, aws.StringValue(s.StackStatus))
			}
			return collectNodeGroupOceanClusterID(s, nodeGroup)
		}
	}

	return nil
}

// collectNodeGroupOceanClusterID collects the Ocean cluster identifier from outputs.
func collectNodeGroupOceanClusterID(stack *cloudformation.Stack, nodeGroup *api.NodeGroup) error {
	if nodeGroup.SpotOcean.Metadata == nil {
		nodeGroup.SpotOcean.Metadata = new(api.NodeGroupSpotOceanMetadata)
	}

	requiredCollectors := map[string]outputs.Collector{
		outputs.NodeGroupSpotOceanClusterID: func(s string) error {
			nodeGroup.SpotOcean.Metadata.ClusterID = aws.String(s)
			return nil
		},
	}

	return outputs.Collect(*stack, requiredCollectors, nil)
}

// nodeGroupName returns the name of the nodegroup.
func nodeGroupName(stack *cloudformation.Stack) string {
	for _, tag := range stack.Tags {
		switch *tag.Key {
		case api.NodeGroupNameTag:
			return *tag.Value
		}
	}

	return ""
}

// nodeGroupStatusIsNotTransitional returns true when nodegroup status is non-transitional.
func nodeGroupStatusIsNotTransitional(stack *cloudformation.Stack) bool {
	states := map[string]struct{}{
		cloudformation.StackStatusCreateComplete:         {},
		cloudformation.StackStatusUpdateComplete:         {},
		cloudformation.StackStatusRollbackComplete:       {},
		cloudformation.StackStatusUpdateRollbackComplete: {},
	}
	_, ok := states[*stack.StackStatus]
	return ok
}
