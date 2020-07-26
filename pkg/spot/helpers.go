package spot

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/kris-nova/logger"
	"github.com/spotinst/spotinst-sdk-go/spotinst"
	"github.com/spotinst/spotinst-sdk-go/spotinst/credentials"
	"github.com/spotinst/spotinst-sdk-go/spotinst/featureflag"
	"github.com/weaveworks/eksctl/pkg/addons"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/authconfigmap"
	"github.com/weaveworks/eksctl/pkg/cfn/outputs"
	"github.com/weaveworks/eksctl/pkg/kubernetes"
	kubewrapper "github.com/weaveworks/eksctl/pkg/kubernetes"
)

// NewNodeGroup creates a new NodeGroup, and returns a pointer to it.
func NewNodeGroup() *api.NodeGroup {
	ng := api.NewNodeGroup()
	ng.SpotOcean = new(api.NodeGroupSpotOcean)
	return ng
}

// NewNodeGroupForOcean creates a new NodeGroup, and returns a pointer to it.
func NewNodeGroupForOcean() *api.NodeGroup {
	ng := NewNodeGroup()
	ng.Name = api.SpotOceanNodeGroupName
	return ng
}

// RunPreCreation executes pre-creation actions.
func RunPreCreation(clusterConfig *api.ClusterConfig, stacks []*cloudformation.Stack) error {
	logger.Debug("spot: executing pre-creation actions")

	for _, ng := range clusterConfig.NodeGroups {
		if ng.SpotOcean == nil || (ng.SpotOcean.Metadata != nil &&
			ng.SpotOcean.Metadata.ClusterID != nil) {
			logger.Debug("spot: skipping nodegroup %q", ng.Name)
			continue
		}
		logger.Debug("spot: handling nodegroup %q", ng.Name)

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
	rawClient *kubewrapper.RawClient, updateAuthConfigMap bool) error {
	logger.Debug("spot: executing post-creation actions")

	for _, ng := range clusterConfig.NodeGroups {
		if ng.SpotOcean == nil {
			logger.Debug("spot: skipping nodegroup %q", ng.Name)
			continue
		}
		logger.Debug("spot: handling nodegroup %q", ng.Name)

		// Allow nodes that are launched by Ocean to join to the cluster.
		// We have to do it before all other nodegroups to prevent `WaitForNodes`
		// to wait forever.
		if updateAuthConfigMap {
			logger.Debug("spot: updating auth configmap")
			if err := authconfigmap.AddNodeGroup(clientSet, ng); err != nil {
				return fmt.Errorf("spot: error updaing auth configmap: %w", err)
			}
		}

		// Install the Ocean controller.
		if ng.Name == api.SpotOceanNodeGroupName {
			logger.Debug("spot: installing addons")
			controller := addons.NewSpotOceanController(
				rawClient,
				clusterConfig,
				false,
				aws.StringValue(ng.SpotOcean.Metadata.Profile))
			if err := controller.Deploy(); err != nil {
				return fmt.Errorf("spot: error installing ocean controller: %w", err)
			}
		}
	}

	return nil
}

// RunPreDeletion executes pre-deletion actions.
func RunPreDeletion(clusterProvider api.ClusterProvider,
	clusterConfig *api.ClusterConfig, nodeGroups []*api.NodeGroup,
	stacks []*cloudformation.Stack, shouldDelete DeleteFilter, plan bool) error {
	logger.Debug("spot: executing pre-deletion actions")

	LoadFeatureFlags()
	if !plan && AllowCredentialsChanges.Enabled() {
		logger.Debug("spot: updating credentials for existing nodegroups")

		for _, ng := range nodeGroups {
			if shouldDelete(ng.Name) && NodeGroupManagedByOcean(ng, stacks) {
				if err := UpdateCredentials(clusterProvider, clusterConfig,
					ng.Name, stacks); err != nil {
					return err
				}
			} else {
				logger.Debug("spot: skipping credentials update for "+
					"nodegroup %q", ng.Name)
			}
		}
	}

	_, shouldDeleteOcean, err := ShouldDeleteOceanNodeGroup(stacks, shouldDelete)
	if err != nil {
		return err
	}
	if shouldDeleteOcean {
		if !plan && AllowCredentialsChanges.Enabled() {
			if err = UpdateCredentials(clusterProvider,
				clusterConfig, api.SpotOceanNodeGroupName, stacks); err != nil {
				return err
			}
		}

		// Allow post-deletion actions to be performed on Ocean as well.
		clusterConfig.NodeGroups = append(clusterConfig.NodeGroups,
			NewNodeGroupForOcean())
	}

	return nil
}

// ErrSpotMultipleDefaultLaunchSpecs represents an error in detecting the default
// nodegroup since more than one has been configured as such.
var ErrSpotMultipleDefaultLaunchSpecs = errors.New("spot: unable to detect " +
	"default ocean launch spec: multiple nodegroups configured with " +
	"`spot.metadata.defaultLaunchSpec: true`")

// ShouldCreateOceanNodeGroup checks whether the nodegroup of the Ocean cluster
// should be created and, if so, returns its NodeGroup configuration.
func ShouldCreateOceanNodeGroup(nodeGroups []*api.NodeGroup) (*api.NodeGroup, bool, error) {
	logger.Debug("spot: checking whether ocean cluster should be created")

	var (
		oceanNodeGroups           = make([]*api.NodeGroup, 0, len(nodeGroups))
		oceanNodeGroup            *api.NodeGroup
		desired, minimum, maximum *int
	)

	// If there are no Ocean nodegroups, let's bail early.
	for _, ng := range nodeGroups {
		if ng.SpotOcean != nil {
			oceanNodeGroups = append(oceanNodeGroups, ng)
		}
	}
	if len(oceanNodeGroups) == 0 {
		return nil, false, nil
	}

	// Find the default nodegroup and calculate the capacity.
	for _, ng := range oceanNodeGroups {
		// Is this the default nodegroup?
		if aws.BoolValue(ng.SpotOcean.Metadata.DefaultLaunchSpec) {
			if oceanNodeGroup != nil {
				logger.Debug("spot: multiple default nodegroups (%q and %q)",
					ng.Name, oceanNodeGroup.Name)
				return nil, false, ErrSpotMultipleDefaultLaunchSpecs
			}
			oceanNodeGroup = ng.DeepCopy()
		}

		// Sum up the capacity from all nodegroups.
		if ng.DesiredCapacity != nil {
			if desired == nil {
				desired = new(int)
			}
			*desired += aws.IntValue(ng.DesiredCapacity)
		}
		if ng.MinSize != nil {
			if minimum == nil {
				minimum = new(int)
			}
			*minimum += aws.IntValue(ng.MinSize)
		}
		if ng.MaxSize != nil {
			if maximum == nil {
				maximum = new(int)
			}
			*maximum += aws.IntValue(ng.MaxSize)
		}
	}

	// No default nodegroup. Take the first one.
	if oceanNodeGroup == nil {
		oceanNodeGroup = oceanNodeGroups[0].DeepCopy()
	}
	logger.Debug("spot: using default nodegroup %q", oceanNodeGroup.Name)

	// Set the capacity.
	oceanNodeGroup.DesiredCapacity = desired
	oceanNodeGroup.MinSize = minimum
	oceanNodeGroup.MaxSize = maximum

	// Default of one node to run cluster-controller/metrics-server.
	if desired == nil && minimum == nil {
		oceanNodeGroup.DesiredCapacity = aws.Int(1)
		oceanNodeGroup.MinSize = aws.Int(1)
	}

	// If there is already an existing cluster, we're done.
	if oceanNodeGroup.SpotOcean.Metadata.ClusterID != nil {
		logger.Debug("spot: ocean cluster already exists")
		return nil, false, nil
	}

	// Configure the nodegroup name.
	oceanNodeGroup.Name = api.SpotOceanNodeGroupName

	logger.Debug("spot: ocean cluster should be created")
	return oceanNodeGroup, true, nil
}

// ShouldDeleteOceanNodeGroup checks whether the nodegroup of the Ocean cluster
// should be deleted and, if so, returns its Cloud Formation stack.
func ShouldDeleteOceanNodeGroup(stacks []*cloudformation.Stack,
	shouldDelete func(string) bool) (*cloudformation.Stack, bool, error) {

	logger.Debug("spot: checking whether ocean cluster should be deleted")
	var oceanNodeGroupStack *cloudformation.Stack

	// If there is no nodegroup for the Ocean cluster, let's bail early.
	for _, s := range stacks {
		if NodeGroupName(s) == api.SpotOceanNodeGroupName {
			logger.Debug("spot: found ocean cluster")
			oceanNodeGroupStack = s
			break
		}
	}
	if oceanNodeGroupStack == nil {
		logger.Debug("spot: ocean cluster does not exist; nothing to delete")
		return nil, false, nil
	}

	// Do not delete if there is at least one nodegroup that is not marked for deletion.
	if !shouldDelete(api.SpotOceanNodeGroupName) {
		for _, s := range stacks {
			ngName := NodeGroupName(s)
			ng := &api.NodeGroup{NodeGroupBase: &api.NodeGroupBase{Name: ngName}}

			if !shouldDelete(ngName) && ngName != api.SpotOceanNodeGroupName &&
				NodeGroupStatusIsNotTransitional(s) && NodeGroupManagedByOcean(ng, stacks) {
				logger.Debug("spot: at least one nodegroup remains "+
					"active (%s); skipping ocean cluster deletion", ngName)
				return nil, false, nil
			}
		}
	}

	logger.Debug("spot: ocean cluster should be deleted")
	return oceanNodeGroupStack, true, nil // all nodegroups are marked for deletion
}

// ensureNodeGroupOceanClusterID retrieves the Ocean cluster identifier.
func ensureNodeGroupOceanClusterID(stacks []*cloudformation.Stack, nodeGroup *api.NodeGroup) error {
	for _, s := range stacks {
		if NodeGroupName(s) == api.SpotOceanNodeGroupName {
			if !NodeGroupStatusIsNotTransitional(s) {
				return fmt.Errorf("spot: nodegroup %q is in transitional "+
					"state %q", nodeGroup.Name, aws.StringValue(s.StackStatus))
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

// NodeGroupName returns the name of the nodegroup.
func NodeGroupName(stack *cloudformation.Stack) string {
	for _, tag := range stack.Tags {
		switch *tag.Key {
		case api.NodeGroupNameTag:
			return *tag.Value
		}
	}
	return ""
}

// NodeGroupFromStacks returns the nodegroup by name.
func NodeGroupFromStacks(name string, stacks []*cloudformation.Stack) *cloudformation.Stack {
	for _, stack := range stacks {
		if NodeGroupName(stack) == name {
			return stack
		}
	}
	return nil
}

// NodeGroupStatusIsNotTransitional returns true when nodegroup status is non-transitional.
func NodeGroupStatusIsNotTransitional(stack *cloudformation.Stack) bool {
	states := map[string]struct{}{
		cloudformation.StackStatusCreateComplete:         {},
		cloudformation.StackStatusUpdateComplete:         {},
		cloudformation.StackStatusRollbackComplete:       {},
		cloudformation.StackStatusUpdateRollbackComplete: {},
	}
	_, ok := states[*stack.StackStatus]
	return ok
}

// NodeGroupManagedByOcean returns a boolean indicating whether the nodegroup is managed by Ocean.
func NodeGroupManagedByOcean(nodeGroup *api.NodeGroup, stacks []*cloudformation.Stack) bool {
	if nodeGroup.SpotOcean != nil { // fast path when using a config file
		return true
	}
	for _, stack := range stacks { // slow path when using a flag
		if nodeGroup.Name != NodeGroupName(stack) {
			continue
		}
		for _, tag := range stack.Tags {
			if aws.StringValue(tag.Key) == api.SpotOceanResourceTypeTag {
				return true
			}
		}
	}
	return false
}

const (
	// Name of the key associated with the parameter that holds the user token.
	CredentialsTokenParameterKey = "SpotToken"
	// Name of the key associated with the parameter that holds the user account.
	CredentialsAccountParameterKey = "SpotAccount"
)

// UpdateCredentials loads the user credentials from its local environment and
// updates the upstream credentials, stored in AWS CloudFormation, by updating
// the stack parameters.  Users should set the `AllowCredentialsChanges` feature
// flag to avoid unnecessary calls caused by updating the AWS CloudFormation
// stack parameters.
func UpdateCredentials(clusterProvider api.ClusterProvider, clusterConfig *api.ClusterConfig,
	ngName string, stacks []*cloudformation.Stack) error {
	logger.Debug("spot: updating credentials for nodegroup %q", ngName)

	// Find the stack by the name of the nodegroup.
	stack := NodeGroupFromStacks(ngName, stacks)
	if stack == nil {
		logger.Debug("spot: couldn't find stack for nodegroup %q", ngName)
		return nil
	}

	// Set the credentials profile, if any.
	var profile *string
	for _, ng := range clusterConfig.NodeGroups {
		if ng.Name == NodeGroupName(stack) && ng.SpotOcean != nil && ng.SpotOcean.Metadata != nil {
			profile = ng.SpotOcean.Metadata.Profile
		}
	}

	// Load user credentials.
	token, account, err := LoadCredentials(profile)
	if err != nil {
		return err
	}

	// Update upstream credentials and reuse the existing template.
	if err := updateUpstreamCredentials(clusterProvider, stack, token, account); err != nil {
		return err
	}

	logger.Debug("spot: successfully updated credentials for nodegroup %q", ngName)
	return nil
}

// updateUpstreamCredentials updates the upstream credentials, stored in AWS
// CloudFormation, by updating the stack parameters.
func updateUpstreamCredentials(clusterProvider api.ClusterProvider,
	stack *cloudformation.Stack, token, account string) error {

	var (
		cfnAPI  = clusterProvider.CloudFormation()
		cfnWait = true
	)

	// Set parameters.
	input := &cloudformation.UpdateStackInput{
		StackName:           stack.StackName,
		Capabilities:        aws.StringSlice([]string{cloudformation.CapabilityCapabilityIam}),
		UsePreviousTemplate: aws.Bool(true),
		Parameters: []*cloudformation.Parameter{
			{
				ParameterKey:   aws.String(CredentialsTokenParameterKey),
				ParameterValue: aws.String(token),
			},
			{
				ParameterKey:   aws.String(CredentialsAccountParameterKey),
				ParameterValue: aws.String(account),
			},
			{
				ParameterKey:   aws.String(FeatureFlagsParameterKey),
				ParameterValue: aws.String(convertFeatureFlags()),
			},
		},
	}

	// Update stack parameters.
	logger.Debug("spot: updating stack %q", aws.StringValue(stack.StackName))
	_, err := cfnAPI.UpdateStack(input)
	if err != nil {
		awsErr, ok := err.(awserr.Error)
		if !ok {
			return err
		}
		if !ignoreUpdateStackError(awsErr.Message()) {
			return err
		}
		cfnWait = false
		logger.Debug("spot: new and old credentials are same; no updates "+
			"are to be performed for stack %q", stack.StackName)
	}

	// Wait until stack status is UPDATE_COMPLETE.
	if cfnWait {
		logger.Debug("spot: waiting for stack update to complete")
		input := &cloudformation.DescribeStacksInput{
			StackName: stack.StackName,
		}

		if err := cfnAPI.WaitUntilStackUpdateComplete(input); err != nil {
			return err
		}
	}

	return nil
}

// ignoreUpdateStackError ignores errors that may occur while updating a stack.
func ignoreUpdateStackError(errMsg string) bool {
	errMsgs := []string{
		"no updates are to be performed",
	}
	for _, msg := range errMsgs {
		if strings.Contains(strings.ToLower(errMsg), msg) {
			return true
		}
	}
	return false
}

// LoadCredentials loads and returns the user credentials.
func LoadCredentials(profile *string) (string, string, error) {
	logger.Debug("spot: loading credentials")

	providers := []credentials.Provider{
		&credentials.EnvProvider{},
		&credentials.FileProvider{Profile: spotinst.StringValue(profile)},
	}

	config := spotinst.DefaultConfig()
	config.WithCredentials(credentials.NewChainCredentials(providers...))

	c, err := config.Credentials.Get()
	if err != nil {
		return "", "", err
	}

	return c.Token, c.Account, nil
}

const (
	// Default ARN of the AWS Lambda function that should handle AWS CloudFormation requests.
	defaultServiceToken = "arn:aws:lambda:${AWS::Region}:178579023202:function:spotinst-cloudformation"
	// Name of the environment variable to read when loading a custom service token.
	envServiceToken = "SPOTINST_SERVICE_TOKEN"
)

// LoadServiceToken loads and returns the service token that should be use by
// AWS CloudFormation.
func LoadServiceToken() (string, error) {
	logger.Debug("spot: loading service token")

	v := os.Getenv(envServiceToken)
	if v == "" {
		v = defaultServiceToken
	}

	logger.Debug("spot: will use service token %q", v)
	return v, nil
}

// AllowCredentialsChanges is a feature flag that controls whether eksctl should
// allow credentials changes.  When true, eksctl reloads the user credentials
// and attempts to update the relevant AWS CloudFormation stacks.
var AllowCredentialsChanges = featureflag.New("AllowCredentialsChanges", false)

// Name of the key associated with the parameter that holds all feature flags.
const FeatureFlagsParameterKey = "SpotFeatureFlags"

// LoadFeatureFlags reads the feature flags from an environment variable.
func LoadFeatureFlags() string {
	featureflag.Set(os.Getenv(featureflag.EnvVar))
	logger.Debug("spot: will use feature flags %q", featureflag.All())

	// Convert to upstream feature flags, if needed.
	return convertFeatureFlags()
}

// convertFeatureFlags returns the upstream feature flags that should be
// configured for the resource handler.
func convertFeatureFlags() string {
	ff := "None" // avoids `Parameters: [SpotFeatureFlags] must have values` errors

	if AllowCredentialsChanges.Enabled() {
		// When the user allows credentials changes, we have to configure the
		// opposite feature flag for the resource handler to avoid unnecessary
		// calls caused by updating the AWS CloudFormation stack parameters.
		ff = "IgnoreCredentialsChanges=true"
	}

	logger.Debug("spot: will set feature flags %q", ff)
	return ff
}

// DeleteFilter represents the type definition for a delete filter.
type DeleteFilter func(ngName string) bool

// NewDeleteAllFilter returns a DeleteFilter that always returns true.
func NewDeleteAllFilter() DeleteFilter {
	return func(_ string) bool {
		return true
	}
}

// NewDeleteIncludedFilter returns a DeleteFilter that returns true whether the
// nodegroup is included.
func NewDeleteIncludedFilter(nodeGroups []*api.NodeGroup) DeleteFilter {
	return func(ngName string) bool {
		for _, ng := range nodeGroups {
			if ng.Name == ngName {
				return true
			}
		}
		return false
	}
}
