package builder

import (
	"encoding/json"
	"fmt"
	"strings"

	cfn "github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	"github.com/aws/aws-sdk-go/service/iam/iamiface"
	"github.com/pkg/errors"
	"github.com/spotinst/spotinst-sdk-go/spotinst"
	gfn "github.com/weaveworks/goformation/v4/cloudformation"
	gfnec2 "github.com/weaveworks/goformation/v4/cloudformation/ec2"
	gfnt "github.com/weaveworks/goformation/v4/cloudformation/types"

	"github.com/kris-nova/logger"

	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/cfn/outputs"
	"github.com/weaveworks/eksctl/pkg/nodebootstrap"
	"github.com/weaveworks/eksctl/pkg/spot"
	"github.com/weaveworks/eksctl/pkg/vpc"
)

// NodeGroupResourceSet stores the resource information of the nodegroup
type NodeGroupResourceSet struct {
	rs                   *resourceSet
	clusterSpec          *api.ClusterConfig
	spec                 *api.NodeGroup
	supportsManagedNodes bool
	forceAddCNIPolicy    bool
	ec2API               ec2iface.EC2API
	iamAPI               iamiface.IAMAPI
	instanceProfileARN   *gfnt.Value
	securityGroups       []*gfnt.Value
	vpc                  *gfnt.Value
	vpcImporter          vpc.Importer
	bootstrapper         nodebootstrap.Bootstrapper
	sharedTags           []*cfn.Tag
}

// NewNodeGroupResourceSet returns a resource set for a nodegroup embedded in a cluster config
func NewNodeGroupResourceSet(ec2API ec2iface.EC2API, iamAPI iamiface.IAMAPI, spec *api.ClusterConfig, ng *api.NodeGroup,
	sharedTags []*cfn.Tag, supportsManagedNodes, forceAddCNIPolicy bool, vpcImporter vpc.Importer) *NodeGroupResourceSet {
	return &NodeGroupResourceSet{
		rs:                   newResourceSet(),
		supportsManagedNodes: supportsManagedNodes,
		forceAddCNIPolicy:    forceAddCNIPolicy,
		clusterSpec:          spec,
		spec:                 ng,
		ec2API:               ec2API,
		iamAPI:               iamAPI,
		vpcImporter:          vpcImporter,
		bootstrapper:         nodebootstrap.NewBootstrapper(spec, ng),
		sharedTags:           sharedTags,
	}
}

// AddAllResources adds all the information about the nodegroup to the resource set
func (n *NodeGroupResourceSet) AddAllResources() error {
	n.rs.template.Description = fmt.Sprintf(
		"%s (AMI family: %s, SSH access: %v, private networking: %v) %s",
		nodeGroupTemplateDescription,
		n.spec.AMIFamily, api.IsEnabled(n.spec.SSH.Allow), n.spec.PrivateNetworking,
		templateDescriptionSuffix)

	n.Template().Mappings[servicePrincipalPartitionMapName] = servicePrincipalPartitionMappings

	n.rs.defineOutputWithoutCollector(outputs.NodeGroupFeaturePrivateNetworking, n.spec.PrivateNetworking, false)
	n.rs.defineOutputWithoutCollector(outputs.NodeGroupFeatureSharedSecurityGroup, n.spec.SecurityGroups.WithShared, false)
	n.rs.defineOutputWithoutCollector(outputs.NodeGroupFeatureLocalSecurityGroup, n.spec.SecurityGroups.WithLocal, false)

	n.vpc = n.vpcImporter.VPC()

	// Ensure MinSize is set, as it is required by the ASG cfn resource
	// TODO this validation and default setting should happen way earlier than this
	if n.spec.MinSize == nil {
		if n.spec.DesiredCapacity == nil {
			defaultNodeCount := api.DefaultNodeCount
			n.spec.MinSize = &defaultNodeCount
		} else {
			n.spec.MinSize = n.spec.DesiredCapacity
		}
		logger.Info("--nodes-min=%d was set automatically for nodegroup %s", *n.spec.MinSize, n.spec.Name)
	} else if n.spec.DesiredCapacity != nil && *n.spec.DesiredCapacity < *n.spec.MinSize {
		return fmt.Errorf("--nodes value (%d) cannot be lower than --nodes-min value (%d)", *n.spec.DesiredCapacity, *n.spec.MinSize)
	}

	// Ensure MaxSize is set, as it is required by the ASG cfn resource
	if n.spec.MaxSize == nil {
		if n.spec.DesiredCapacity == nil {
			n.spec.MaxSize = n.spec.MinSize
		} else {
			n.spec.MaxSize = n.spec.DesiredCapacity
		}
		logger.Info("--nodes-max=%d was set automatically for nodegroup %s", *n.spec.MaxSize, n.spec.Name)
	} else if n.spec.DesiredCapacity != nil && *n.spec.DesiredCapacity > *n.spec.MaxSize {
		return fmt.Errorf("--nodes value (%d) cannot be greater than --nodes-max value (%d)", *n.spec.DesiredCapacity, *n.spec.MaxSize)
	} else if *n.spec.MaxSize < *n.spec.MinSize {
		return fmt.Errorf("--nodes-min value (%d) cannot be greater than --nodes-max value (%d)", *n.spec.MinSize, *n.spec.MaxSize)
	}

	if err := n.addResourcesForIAM(); err != nil {
		return err
	}
	n.addResourcesForSecurityGroups()

	return n.addResourcesForNodeGroup()
}

// RenderJSON returns the rendered JSON
func (n *NodeGroupResourceSet) RenderJSON() ([]byte, error) {
	return n.rs.renderJSON()
}

// Template returns the CloudFormation template
func (n *NodeGroupResourceSet) Template() gfn.Template {
	return *n.rs.template
}

func (n *NodeGroupResourceSet) newResource(name string, resource gfn.Resource) *gfnt.Value {
	return n.rs.newResource(name, resource)
}

func (n *NodeGroupResourceSet) addResourcesForNodeGroup() error {
	launchTemplateName := gfnt.MakeFnSubString(fmt.Sprintf("${%s}", gfnt.StackName))
	launchTemplateData, err := n.newLaunchTemplateData()
	if err != nil {
		return errors.Wrap(err, "could not add resources for nodegroup")
	}

	if n.spec.SSH != nil && api.IsSetAndNonEmptyString(n.spec.SSH.PublicKeyName) {
		launchTemplateData.KeyName = gfnt.NewString(*n.spec.SSH.PublicKeyName)
	}

	if volumeSize := n.spec.VolumeSize; volumeSize != nil && *volumeSize > 0 {
		var (
			kmsKeyID         *gfnt.Value
			volumeIOPS       *gfnt.Value
			volumeThroughput *gfnt.Value
			volumeType       = *n.spec.VolumeType
		)

		if api.IsSetAndNonEmptyString(n.spec.VolumeKmsKeyID) {
			kmsKeyID = gfnt.NewString(*n.spec.VolumeKmsKeyID)
		}

		if volumeType == api.NodeVolumeTypeIO1 || volumeType == api.NodeVolumeTypeGP3 {
			volumeIOPS = gfnt.NewInteger(*n.spec.VolumeIOPS)
		}

		if volumeType == api.NodeVolumeTypeGP3 {
			volumeThroughput = gfnt.NewInteger(*n.spec.VolumeThroughput)
		}

		launchTemplateData.BlockDeviceMappings = []gfnec2.LaunchTemplate_BlockDeviceMapping{{
			DeviceName: gfnt.NewString(*n.spec.VolumeName),
			Ebs: &gfnec2.LaunchTemplate_Ebs{
				VolumeSize: gfnt.NewInteger(*volumeSize),
				VolumeType: gfnt.NewString(volumeType),
				Encrypted:  gfnt.NewBoolean(*n.spec.VolumeEncrypted),
				KmsKeyId:   kmsKeyID,
				Iops:       volumeIOPS,
				Throughput: volumeThroughput,
			},
		}}

		if n.spec.AdditionalEncryptedVolume != "" {
			launchTemplateData.BlockDeviceMappings = append(launchTemplateData.BlockDeviceMappings, gfnec2.LaunchTemplate_BlockDeviceMapping{
				DeviceName: gfnt.NewString(n.spec.AdditionalEncryptedVolume),
				Ebs: &gfnec2.LaunchTemplate_Ebs{
					Encrypted: gfnt.NewBoolean(*n.spec.VolumeEncrypted),
					KmsKeyId:  kmsKeyID,
				},
			})
		}
	}

	launchTemplate := &gfnec2.LaunchTemplate{
		LaunchTemplateName: launchTemplateName,
		LaunchTemplateData: launchTemplateData,
	}

	// Do not create a Launch Template resource for Spot-managed nodegroups.
	if n.spec.SpotOcean == nil {
		n.newResource("NodeGroupLaunchTemplate", launchTemplate)
	}

	vpcZoneIdentifier, err := AssignSubnets(n.spec.NodeGroupBase, n.vpcImporter, n.clusterSpec)
	if err != nil {
		return err
	}

	tags := []map[string]interface{}{
		{
			"Key":               "Name",
			"Value":             generateNodeName(n.spec.NodeGroupBase, n.clusterSpec.Metadata),
			"PropagateAtLaunch": "true",
		},
		{
			"Key":               "kubernetes.io/cluster/" + n.clusterSpec.Metadata.Name,
			"Value":             "owned",
			"PropagateAtLaunch": "true",
		},
	}
	if api.IsEnabled(n.spec.IAM.WithAddonPolicies.AutoScaler) {
		tags = append(tags,
			map[string]interface{}{
				"Key":               "k8s.io/cluster-autoscaler/enabled",
				"Value":             "true",
				"PropagateAtLaunch": "true",
			},
			map[string]interface{}{
				"Key":               "k8s.io/cluster-autoscaler/" + n.clusterSpec.Metadata.Name,
				"Value":             "owned",
				"PropagateAtLaunch": "true",
			},
		)
	}

	g, err := n.newNodeGroupResource(launchTemplate, &vpcZoneIdentifier, tags)
	if err != nil {
		return fmt.Errorf("failed to build nodegroup resource: %v", err)
	}
	n.newResource("NodeGroup", g)

	return nil
}

// generateNodeName formulates the name based on the configuration in input
func generateNodeName(ng *api.NodeGroupBase, meta *api.ClusterMeta) string {
	var nameParts []string
	if ng.InstancePrefix != "" {
		nameParts = append(nameParts, ng.InstancePrefix, "-")
	}
	// this overrides the default naming convention
	if ng.InstanceName != "" {
		nameParts = append(nameParts, ng.InstanceName)
	} else {
		nameParts = append(nameParts, fmt.Sprintf("%s-%s-Node", meta.Name, ng.Name))
	}
	return strings.Join(nameParts, "")
}

// AssignSubnets subnets based on the specified availability zones
func AssignSubnets(spec *api.NodeGroupBase, vpcImporter vpc.Importer, clusterSpec *api.ClusterConfig) (*gfnt.Value, error) {
	// currently goformation type system doesn't allow specifying `VPCZoneIdentifier: { "Fn::ImportValue": ... }`,
	// and tags don't have `PropagateAtLaunch` field, so we have a custom method here until this gets resolved

	if len(spec.AvailabilityZones) > 0 || len(spec.Subnets) > 0 || api.IsEnabled(spec.EFAEnabled) {
		subnets := clusterSpec.VPC.Subnets.Public
		typ := "public"
		if spec.PrivateNetworking {
			subnets = clusterSpec.VPC.Subnets.Private
			typ = "private"
		}
		subnetIDs, err := vpc.SelectNodeGroupSubnets(spec.AvailabilityZones, spec.Subnets, subnets)
		if api.IsEnabled(spec.EFAEnabled) && len(subnetIDs) > 1 {
			subnetIDs = []string{subnetIDs[0]}
			logger.Info("EFA requires all nodes be in a single subnet, arbitrarily choosing one: %s", subnetIDs)
		}
		return gfnt.NewStringSlice(subnetIDs...), errors.Wrapf(err, "couldn't find %s subnets", typ)
	}

	var subnets *gfnt.Value
	if spec.PrivateNetworking {
		subnets = vpcImporter.SubnetsPrivate()
	} else {
		subnets = vpcImporter.SubnetsPublic()
	}

	return subnets, nil
}

// GetAllOutputs collects all outputs of the nodegroup
func (n *NodeGroupResourceSet) GetAllOutputs(stack cfn.Stack) error {
	return n.rs.GetAllOutputs(stack)
}

func (n *NodeGroupResourceSet) newLaunchTemplateData() (*gfnec2.LaunchTemplate_LaunchTemplateData, error) {
	userData, err := n.bootstrapper.UserData()
	if err != nil {
		return nil, err
	}

	launchTemplateData := &gfnec2.LaunchTemplate_LaunchTemplateData{
		IamInstanceProfile: &gfnec2.LaunchTemplate_IamInstanceProfile{
			Arn: n.instanceProfileARN,
		},
		ImageId:         gfnt.NewString(n.spec.AMI),
		UserData:        gfnt.NewString(userData),
		MetadataOptions: makeMetadataOptions(n.spec.NodeGroupBase),
	}

	if err := buildNetworkInterfaces(launchTemplateData, n.spec.InstanceTypeList(), api.IsEnabled(n.spec.EFAEnabled), n.securityGroups, n.ec2API); err != nil {
		return nil, errors.Wrap(err, "couldn't build network interfaces for launch template data")
	}

	if api.IsEnabled(n.spec.EFAEnabled) && n.spec.Placement == nil {
		groupName := n.newResource("NodeGroupPlacementGroup", &gfnec2.PlacementGroup{
			Strategy: gfnt.NewString("cluster"),
		})
		launchTemplateData.Placement = &gfnec2.LaunchTemplate_Placement{
			GroupName: groupName,
		}
	}

	if !api.HasMixedInstances(n.spec) {
		launchTemplateData.InstanceType = gfnt.NewString(n.spec.InstanceType)
	} else {
		launchTemplateData.InstanceType = gfnt.NewString(n.spec.InstancesDistribution.InstanceTypes[0])
	}
	if n.spec.EBSOptimized != nil {
		launchTemplateData.EbsOptimized = gfnt.NewBoolean(*n.spec.EBSOptimized)
	}

	if n.spec.CPUCredits != nil {
		launchTemplateData.CreditSpecification = &gfnec2.LaunchTemplate_CreditSpecification{
			CpuCredits: gfnt.NewString(strings.ToLower(*n.spec.CPUCredits)),
		}
	}

	if n.spec.Placement != nil {
		launchTemplateData.Placement = &gfnec2.LaunchTemplate_Placement{
			GroupName: gfnt.NewString(n.spec.Placement.GroupName),
		}
	}

	return launchTemplateData, nil
}

func makeMetadataOptions(ng *api.NodeGroupBase) *gfnec2.LaunchTemplate_MetadataOptions {
	imdsv2TokensRequired := "optional"
	if api.IsEnabled(ng.DisableIMDSv1) || api.IsEnabled(ng.DisablePodIMDS) {
		imdsv2TokensRequired = "required"
	}
	hopLimit := 2
	if api.IsEnabled(ng.DisablePodIMDS) {
		hopLimit = 1
	}
	return &gfnec2.LaunchTemplate_MetadataOptions{
		HttpPutResponseHopLimit: gfnt.NewInteger(hopLimit),
		HttpTokens:              gfnt.NewString(imdsv2TokensRequired),
	}
}

func (n *NodeGroupResourceSet) newNodeGroupResource(launchTemplate *gfnec2.LaunchTemplate,
	vpcZoneIdentifier interface{}, tags []map[string]interface{}) (*awsCloudFormationResource, error) {

	if n.spec.SpotOcean != nil {
		return n.newNodeGroupSpotOceanResource(launchTemplate, vpcZoneIdentifier, tags)
	}

	return n.newNodeGroupAutoScalingGroupResource(launchTemplate, vpcZoneIdentifier, tags)
}

func (n *NodeGroupResourceSet) newNodeGroupAutoScalingGroupResource(launchTemplate *gfnec2.LaunchTemplate,
	vpcZoneIdentifier interface{}, tags []map[string]interface{}) (*awsCloudFormationResource, error) {
	ng := n.spec
	ngProps := map[string]interface{}{
		"VPCZoneIdentifier": vpcZoneIdentifier,
		"Tags":              tags,
	}

	if ng.InstancesDistribution != nil && ng.InstancesDistribution.CapacityRebalance {
		ngProps["CapacityRebalance"] = ng.InstancesDistribution.CapacityRebalance
	}

	if ng.DesiredCapacity != nil {
		ngProps["DesiredCapacity"] = fmt.Sprintf("%d", *ng.DesiredCapacity)
	}
	if ng.MinSize != nil {
		ngProps["MinSize"] = fmt.Sprintf("%d", *ng.MinSize)
	}
	if ng.MaxSize != nil {
		ngProps["MaxSize"] = fmt.Sprintf("%d", *ng.MaxSize)
	}
	if len(ng.ASGMetricsCollection) > 0 {
		ngProps["MetricsCollection"] = metricsCollectionResource(ng.ASGMetricsCollection)
	}
	if len(ng.ClassicLoadBalancerNames) > 0 {
		ngProps["LoadBalancerNames"] = ng.ClassicLoadBalancerNames
	}
	if len(ng.TargetGroupARNs) > 0 {
		ngProps["TargetGroupARNs"] = ng.TargetGroupARNs
	}
	if api.HasMixedInstances(ng) {
		ngProps["MixedInstancesPolicy"] = n.mixedInstancesPolicy(launchTemplate.LaunchTemplateName)
	} else {
		ngProps["LaunchTemplate"] = map[string]interface{}{
			"LaunchTemplateName": launchTemplate.LaunchTemplateName,
			"Version":            gfnt.MakeFnGetAttString("NodeGroupLaunchTemplate", "LatestVersionNumber"),
		}
	}

	rollingUpdate := map[string]interface{}{}
	if len(ng.ASGSuspendProcesses) > 0 {
		rollingUpdate["SuspendProcesses"] = ng.ASGSuspendProcesses
	}

	return &awsCloudFormationResource{
		Type:       "AWS::AutoScaling::AutoScalingGroup",
		Properties: ngProps,
		UpdatePolicy: map[string]map[string]interface{}{
			"AutoScalingRollingUpdate": rollingUpdate,
		},
	}, nil
}

func (n *NodeGroupResourceSet) mixedInstancesPolicy(launchTemplateName *gfnt.Value) map[string]interface{} {
	ng := n.spec
	instanceTypes := ng.InstancesDistribution.InstanceTypes
	overrides := make([]map[string]string, len(instanceTypes))

	for i, instanceType := range instanceTypes {
		overrides[i] = map[string]string{
			"InstanceType": instanceType,
		}
	}
	policy := map[string]interface{}{
		"LaunchTemplate": map[string]interface{}{
			"LaunchTemplateSpecification": map[string]interface{}{
				"LaunchTemplateName": launchTemplateName,
				"Version":            gfnt.MakeFnGetAttString("NodeGroupLaunchTemplate", "LatestVersionNumber"),
			},

			"Overrides": overrides,
		},
	}

	instancesDistribution := map[string]string{}

	// Only set the price if it was specified so otherwise AWS picks "on-demand price" as the default
	if ng.InstancesDistribution.MaxPrice != nil {
		instancesDistribution["SpotMaxPrice"] = fmt.Sprintf("%f", *ng.InstancesDistribution.MaxPrice)
	}
	if ng.InstancesDistribution.OnDemandBaseCapacity != nil {
		instancesDistribution["OnDemandBaseCapacity"] = fmt.Sprintf("%d", *ng.InstancesDistribution.OnDemandBaseCapacity)
	}
	if ng.InstancesDistribution.OnDemandPercentageAboveBaseCapacity != nil {
		instancesDistribution["OnDemandPercentageAboveBaseCapacity"] = fmt.Sprintf("%d", *ng.InstancesDistribution.OnDemandPercentageAboveBaseCapacity)
	}
	if ng.InstancesDistribution.SpotInstancePools != nil {
		instancesDistribution["SpotInstancePools"] = fmt.Sprintf("%d", *ng.InstancesDistribution.SpotInstancePools)
	}

	if ng.InstancesDistribution.SpotAllocationStrategy != nil {
		instancesDistribution["SpotAllocationStrategy"] = *ng.InstancesDistribution.SpotAllocationStrategy
	}

	policy["InstancesDistribution"] = instancesDistribution

	return policy
}

func metricsCollectionResource(asgMetricsCollection []api.MetricsCollection) []map[string]interface{} {
	var metricsCollections []map[string]interface{}
	for _, m := range asgMetricsCollection {
		newCollection := make(map[string]interface{})

		if len(m.Metrics) > 0 {
			newCollection["Metrics"] = m.Metrics
		}
		newCollection["Granularity"] = m.Granularity

		metricsCollections = append(metricsCollections, newCollection)
	}
	return metricsCollections
}

// newNodeGroupSpotOceanResource returns a Spot Ocean resource.
func (n *NodeGroupResourceSet) newNodeGroupSpotOceanResource(launchTemplate *gfnec2.LaunchTemplate,
	vpcZoneIdentifier interface{}, tags []map[string]interface{}) (*awsCloudFormationResource, error) {

	var res *spot.NodeGroupResource
	var out awsCloudFormationResource
	var err error

	// Resource.
	{
		if n.spec.Name == api.SpotOceanNodeGroupName {
			logger.Debug("ocean: building nodegroup %q as cluster", n.spec.Name)
			res, err = n.newNodeGroupSpotOceanClusterResource(
				launchTemplate, vpcZoneIdentifier, tags)
		} else {
			logger.Debug("ocean: building nodegroup %q as launchspec", n.spec.Name)
			res, err = n.newNodeGroupSpotOceanLaunchSpecResource(
				launchTemplate, vpcZoneIdentifier, tags)
		}
		if err != nil {
			return nil, err
		}
	}

	// Service Token.
	{
		res.ServiceToken = gfnt.MakeFnSubString(spot.LoadServiceToken())
	}

	// Credentials.
	{
		token, account, err := spot.LoadCredentials()
		if err != nil {
			return nil, err
		}
		if token != "" {
			res.Token = n.rs.newParameter(spot.CredentialsTokenParameterKey, gfn.Parameter{
				Type:    "String",
				Default: token,
			})
		}
		if account != "" {
			res.Account = n.rs.newParameter(spot.CredentialsAccountParameterKey, gfn.Parameter{
				Type:    "String",
				Default: account,
			})
		}
	}

	// Feature Flags.
	{
		if ff := spot.LoadFeatureFlags(); ff != "" {
			res.FeatureFlags = n.rs.newParameter(spot.FeatureFlagsParameterKey, gfn.Parameter{
				Type:    "String",
				Default: ff,
			})
		}
	}

	// Convert.
	{
		b, err := json.Marshal(res)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, err
		}
	}

	return &out, nil
}

// newNodeGroupSpotOceanClusterResource returns a Spot Ocean cluster resource.
func (n *NodeGroupResourceSet) newNodeGroupSpotOceanClusterResource(launchTemplate *gfnec2.LaunchTemplate,
	vpcZoneIdentifier interface{}, resourceTags []map[string]interface{}) (*spot.NodeGroupResource, error) {

	template := launchTemplate.LaunchTemplateData
	cluster := &spot.Cluster{
		Name:      spotinst.String(n.clusterSpec.Metadata.Name),
		ClusterID: spotinst.String(n.clusterSpec.Metadata.Name),
		Region:    gfnt.MakeRef("AWS::Region"),
		Compute: &spot.Compute{
			LaunchSpecification: &spot.LaunchSpec{
				ImageID:           template.ImageId,
				UserData:          template.UserData,
				KeyPair:           template.KeyName,
				EBSOptimized:      n.spec.EBSOptimized,
				UseAsTemplateOnly: spotinst.Bool(true),
			},
			SubnetIDs: vpcZoneIdentifier,
		},
	}

	// Strategy.
	{
		if strategy := n.spec.SpotOcean.Strategy; strategy != nil {
			cluster.Strategy = &spot.Strategy{
				UtilizeReservedInstances: strategy.UtilizeReservedInstances,
				UtilizeCommitments:       strategy.UtilizeCommitments,
				FallbackToOnDemand:       strategy.FallbackToOnDemand,
			}
		}
	}

	// Storage.
	{
		if n.spec.VolumeSize != nil && spotinst.IntValue(n.spec.VolumeSize) > 0 {
			cluster.Compute.LaunchSpecification.VolumeSize = n.spec.VolumeSize
		}
	}

	// IAM.
	{
		if template.IamInstanceProfile != nil {
			cluster.Compute.LaunchSpecification.IAMInstanceProfile = map[string]*gfnt.Value{
				"arn": template.IamInstanceProfile.Arn,
			}
		}
	}

	// Networking.
	{
		if ifaces := template.NetworkInterfaces; len(ifaces) > 0 {
			if ifaces[0].AssociatePublicIpAddress != nil {
				cluster.Compute.LaunchSpecification.AssociatePublicIPAddress = ifaces[0].AssociatePublicIpAddress
			}
			if ifaces[0].Groups != nil {
				cluster.Compute.LaunchSpecification.SecurityGroupIDs = ifaces[0].Groups
			}
		}
	}

	// Load Balancers.
	{
		var lbs []*spot.LoadBalancer

		// ELBs.
		if len(n.spec.ClassicLoadBalancerNames) > 0 {
			for _, name := range n.spec.ClassicLoadBalancerNames {
				lbs = append(lbs, &spot.LoadBalancer{
					Type: spotinst.String("CLASSIC"),
					Name: spotinst.String(name),
				})
			}
		}

		// ALBs.
		if len(n.spec.TargetGroupARNs) > 0 {
			for _, arn := range n.spec.TargetGroupARNs {
				lbs = append(lbs, &spot.LoadBalancer{
					Type: spotinst.String("TARGET_GROUP"),
					ARN:  spotinst.String(arn),
				})
			}
		}

		if len(lbs) > 0 {
			cluster.Compute.LaunchSpecification.LoadBalancers = lbs
		}
	}

	// Tags.
	{
		var tags []*spot.Tag

		// Nodegroup tags.
		if len(n.spec.Tags) > 0 {
			for key, value := range n.spec.Tags {
				tags = append(tags, &spot.Tag{
					Key:   spotinst.String(key),
					Value: spotinst.String(value),
				})
			}
		}

		// Resource tags (Name, kubernetes.io/*, k8s.io/*, etc.).
		if len(resourceTags) > 0 {
			for _, tag := range resourceTags {
				tags = append(tags, &spot.Tag{
					Key:   tag["Key"],
					Value: tag["Value"],
				})
			}
		}

		// Shared tags (metadata.tags + eksctl's tags).
		if len(n.sharedTags) > 0 {
			for _, tag := range n.sharedTags {
				tags = append(tags, &spot.Tag{
					Key:   spotinst.StringValue(tag.Key),
					Value: spotinst.StringValue(tag.Value),
				})
			}
		}

		if len(tags) > 0 {
			cluster.Compute.LaunchSpecification.Tags = tags
		}
	}

	// Instance Types.
	{
		if compute := n.spec.SpotOcean.Compute; compute != nil && compute.InstanceTypes != nil {
			cluster.Compute.InstanceTypes = &spot.InstanceTypes{
				Whitelist: compute.InstanceTypes.Whitelist,
				Blacklist: compute.InstanceTypes.Blacklist,
			}
		}
	}

	// Scheduling.
	{
		if scheduling := n.spec.SpotOcean.Scheduling; scheduling != nil {
			if hours := scheduling.ShutdownHours; hours != nil {
				cluster.Scheduling = &spot.Scheduling{
					ShutdownHours: &spot.ShutdownHours{
						IsEnabled:   hours.IsEnabled,
						TimeWindows: hours.TimeWindows,
					},
				}
			}

			if tasks := scheduling.Tasks; len(tasks) > 0 {
				if cluster.Scheduling == nil {
					cluster.Scheduling = new(spot.Scheduling)
				}

				cluster.Scheduling.Tasks = make([]*spot.Task, len(tasks))
				for i, task := range tasks {
					cluster.Scheduling.Tasks[i] = &spot.Task{
						IsEnabled:      task.IsEnabled,
						Type:           task.Type,
						CronExpression: task.CronExpression,
					}
				}
			}
		}
	}

	// Auto Scaler.
	{
		if autoScaler := n.spec.SpotOcean.AutoScaler; autoScaler != nil {
			cluster.AutoScaler = &spot.AutoScaler{
				IsEnabled:    autoScaler.Enabled,
				IsAutoConfig: autoScaler.AutoConfig,
				Cooldown:     autoScaler.Cooldown,
			}

			if headrooms := autoScaler.Headrooms; len(headrooms) > 0 {
				cluster.AutoScaler.Headroom = &spot.Headroom{
					CPUPerUnit:    headrooms[0].CPUPerUnit,
					GPUPerUnit:    headrooms[0].GPUPerUnit,
					MemoryPerUnit: headrooms[0].MemoryPerUnit,
					NumOfUnits:    headrooms[0].NumOfUnits,
				}
			}
		}
	}

	// Outputs.
	{
		n.rs.defineOutputWithoutCollector(
			outputs.NodeGroupSpotOceanClusterID,
			gfnt.MakeRef("NodeGroup"),
			true)
	}

	return &spot.NodeGroupResource{Cluster: cluster}, nil
}

// newNodeGroupSpotOceanLaunchSpecResource returns a Spot Ocean launchspec resource.
func (n *NodeGroupResourceSet) newNodeGroupSpotOceanLaunchSpecResource(launchTemplate *gfnec2.LaunchTemplate,
	vpcZoneIdentifier interface{}, resourceTags []map[string]interface{}) (*spot.NodeGroupResource, error) {

	// Import the Ocean cluster identifier.
	oceanClusterStackName := fmt.Sprintf("eksctl-%s-nodegroup-ocean", n.clusterSpec.Metadata.Name)
	oceanClusterID := gfnt.MakeFnImportValueString(fmt.Sprintf("%s::%s",
		oceanClusterStackName,
		outputs.NodeGroupSpotOceanClusterID))

	template := launchTemplate.LaunchTemplateData
	spec := &spot.LaunchSpec{
		Name:      spotinst.String(n.spec.Name),
		OceanID:   oceanClusterID,
		ImageID:   template.ImageId,
		UserData:  template.UserData,
		SubnetIDs: vpcZoneIdentifier,
	}

	// Strategy.
	{
		if strategy := n.spec.SpotOcean.Strategy; strategy != nil {
			spec.Strategy = &spot.Strategy{
				SpotPercentage: strategy.SpotPercentage,
			}
		}
	}

	// Storage.
	{
		if n.spec.VolumeSize != nil && spotinst.IntValue(n.spec.VolumeSize) > 0 {
			var volumeKMSKeyID *string
			var volumeIOPS *int
			if api.IsSetAndNonEmptyString(n.spec.VolumeKmsKeyID) {
				volumeKMSKeyID = n.spec.VolumeKmsKeyID
			}
			if *n.spec.VolumeType == api.NodeVolumeTypeIO1 {
				volumeIOPS = n.spec.VolumeIOPS
			}
			spec.BlockDeviceMappings = []*spot.BlockDevice{{
				DeviceName: n.spec.VolumeName,
				EBS: &spot.BlockDeviceEBS{
					VolumeSize: n.spec.VolumeSize,
					VolumeType: n.spec.VolumeType,
					Encrypted:  n.spec.VolumeEncrypted,
					KMSKeyID:   volumeKMSKeyID,
					IOPS:       volumeIOPS,
				},
			}}
		}
	}

	// IAM.
	{
		if template.IamInstanceProfile != nil {
			spec.IAMInstanceProfile = map[string]*gfnt.Value{
				"arn": template.IamInstanceProfile.Arn,
			}
		}
	}

	// Networking.
	{
		if ifaces := template.NetworkInterfaces; len(ifaces) > 0 {
			if ifaces[0].AssociatePublicIpAddress != nil {
				spec.AssociatePublicIPAddress = ifaces[0].AssociatePublicIpAddress
			}
			if ifaces[0].Groups != nil {
				spec.SecurityGroupIDs = ifaces[0].Groups
			}
		}
	}

	// Tags.
	{
		var tags []*spot.Tag

		// Nodegroup tags.
		if len(n.spec.Tags) > 0 {
			for key, value := range n.spec.Tags {
				tags = append(tags, &spot.Tag{
					Key:   spotinst.String(key),
					Value: spotinst.String(value),
				})
			}
		}

		// Resource tags (Name, kubernetes.io/*, k8s.io/*, etc.).
		if len(resourceTags) > 0 {
			for _, tag := range resourceTags {
				tags = append(tags, &spot.Tag{
					Key:   tag["Key"],
					Value: tag["Value"],
				})
			}
		}

		// Shared tags (metadata.tags + eksctl's tags).
		if len(n.sharedTags) > 0 {
			for _, tag := range n.sharedTags {
				tags = append(tags, &spot.Tag{
					Key:   spotinst.StringValue(tag.Key),
					Value: spotinst.StringValue(tag.Value),
				})
			}
		}

		if len(tags) > 0 {
			spec.Tags = tags
		}
	}

	// Instance Types.
	{
		if compute := n.spec.SpotOcean.Compute; compute != nil && compute.InstanceTypes != nil {
			// Defaults to launchspec instance types.
			types := compute.InstanceTypes.Types

			// If no launchspec instance types are defined for a nodegroup, use
			// the cluster level whitelist to maintain backward compatibility.
			if len(compute.InstanceTypes.Types) == 0 {
				types = compute.InstanceTypes.Whitelist
			}

			spec.InstanceTypes = types
		}
	}

	// Labels.
	{
		if len(n.spec.Labels) > 0 {
			labels := make([]*spot.Label, 0, len(n.spec.Labels))

			for key, value := range n.spec.Labels {
				labels = append(labels, &spot.Label{
					Key:   spotinst.String(key),
					Value: spotinst.String(value),
				})
			}

			spec.Labels = labels
		}
	}

	// Taints.
	{
		if len(n.spec.Taints) > 0 {
			taints := make([]*spot.Taint, 0, len(n.spec.Taints))

			for key, valueEffect := range n.spec.Taints {
				taint := &spot.Taint{
					Key: spotinst.String(key),
				}
				parts := strings.Split(valueEffect, ":")
				if len(parts) >= 1 {
					taint.Value = spotinst.String(parts[0])
				}
				if len(parts) > 1 {
					taint.Effect = spotinst.String(parts[1])
				}
				taints = append(taints, taint)
			}

			spec.Taints = taints
		}
	}

	// Auto Scaler.
	{
		if autoScaler := n.spec.SpotOcean.AutoScaler; autoScaler != nil && len(autoScaler.Headrooms) > 0 {
			headrooms := make([]*spot.Headroom, len(autoScaler.Headrooms))

			for i, headroom := range autoScaler.Headrooms {
				headrooms[i] = &spot.Headroom{
					CPUPerUnit:    headroom.CPUPerUnit,
					GPUPerUnit:    headroom.GPUPerUnit,
					MemoryPerUnit: headroom.MemoryPerUnit,
					NumOfUnits:    headroom.NumOfUnits,
				}
			}

			spec.AutoScaler = &spot.AutoScaler{
				Headrooms: headrooms,
			}
		}
	}

	// Outputs.
	{
		n.rs.defineOutputWithoutCollector(
			outputs.NodeGroupSpotOceanLaunchSpecID,
			gfnt.MakeRef("NodeGroup"),
			true)
	}

	return &spot.NodeGroupResource{
		LaunchSpec: spec,
		Resource: spot.Resource{
			Parameters: spot.ResourceParameters{
				OnCreate: map[string]interface{}{"initialNodes": spotinst.IntValue(n.spec.MinSize)},
				OnDelete: map[string]interface{}{"forceDelete": true},
			},
		},
	}, nil
}
