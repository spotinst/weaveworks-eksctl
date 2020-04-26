package builder

import (
	"encoding/json"
	"fmt"
	"strings"

	gfn "github.com/awslabs/goformation/cloudformation"
	"github.com/kris-nova/logger"
	"github.com/spotinst/spotinst-sdk-go/spotinst"
	"github.com/spotinst/spotinst-sdk-go/spotinst/credentials"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/cfn/outputs"
)

type (
	nodeGroupSpotOceanResource struct {
		nodeGroupSpotOceanBase

		OceanCluster    *nodeGroupSpotOceanCluster    `json:"ocean,omitempty"`
		OceanLaunchSpec *nodeGroupSpotOceanLaunchSpec `json:"oceanLaunchSpec,omitempty"`

		// for internal use only; used by `eksctl get nodegroup` command.
		OceanSummary *nodeGroupSpotOceanSummary `json:"oceanSummary,omitempty"`
	}

	nodeGroupSpotOceanBase struct {
		ServiceToken *gfn.Value `json:"ServiceToken,omitempty"`
		Token        *string    `json:"accessToken,omitempty"`
		Account      *string    `json:"accountId,omitempty"`
	}

	nodeGroupSpotOceanCluster struct {
		Name       *string                       `json:"name,omitempty"`
		ClusterID  *string                       `json:"controllerClusterId,omitempty"`
		Region     *gfn.Value                    `json:"region,omitempty"`
		Capacity   *nodeGroupSpotOceanCapacity   `json:"capacity,omitempty"`
		Strategy   *nodeGroupSpotOceanStrategy   `json:"strategy,omitempty"`
		Compute    *nodeGroupSpotOceanCompute    `json:"compute,omitempty"`
		Scheduling *nodeGroupSpotOceanScheduling `json:"scheduling,omitempty"`
		AutoScaler *nodeGroupSpotOceanAutoScaler `json:"autoScaler,omitempty"`
	}

	nodeGroupSpotOceanLaunchSpec struct {
		Name                     *string                           `json:"name,omitempty"`
		OceanID                  *gfn.Value                        `json:"oceanId,omitempty"`
		ImageID                  *gfn.Value                        `json:"imageId,omitempty"`
		UserData                 *gfn.Value                        `json:"userData,omitempty"`
		KeyPair                  *gfn.Value                        `json:"keyPair,omitempty"`
		AssociatePublicIPAddress *gfn.Value                        `json:"associatePublicIpAddress,omitempty"`
		VolumeSize               *int                              `json:"rootVolumeSize,omitempty"`
		EBSOptimized             *bool                             `json:"ebsOptimized,omitempty"`
		SubnetIDs                interface{}                       `json:"subnetIds,omitempty"`
		IAMInstanceProfile       map[string]*gfn.Value             `json:"iamInstanceProfile,omitempty"`
		SecurityGroupIDs         []*gfn.Value                      `json:"securityGroupIds,omitempty"`
		Tags                     []*nodeGroupSpotOceanTag          `json:"tags,omitempty"`
		LoadBalancers            []*nodeGroupSpotOceanLoadBalancer `json:"loadBalancers,omitempty"`
		Labels                   []*nodeGroupSpotOceanLabel        `json:"labels,omitempty"`
		Taints                   []*nodeGroupSpotOceanTaint        `json:"taints,omitempty"`
		AutoScaler               *nodeGroupSpotOceanAutoScaler     `json:"autoScale,omitempty"`
	}

	nodeGroupSpotOceanSummary struct {
		ImageID  *gfn.Value                  `json:"imageId,omitempty"`
		Capacity *nodeGroupSpotOceanCapacity `json:"capacity,omitempty"`
	}

	nodeGroupSpotOceanCapacity struct {
		Minimum *int `json:"minimum,omitempty"`
		Maximum *int `json:"maximum,omitempty"`
		Target  *int `json:"target,omitempty"`
	}

	nodeGroupSpotOceanStrategy struct {
		SpotPercentage           *int  `json:"spotPercentage,omitempty"`
		UtilizeReservedInstances *bool `json:"utilizeReservedInstances,omitempty"`
		FallbackToOnDemand       *bool `json:"fallbackToOd,omitempty"`
		DrainingTimeout          *int  `json:"drainingTimeout,omitempty"`
	}

	nodeGroupSpotOceanCompute struct {
		SubnetIDs           interface{}                      `json:"subnetIds,omitempty"`
		InstanceTypes       *nodeGroupSpotOceanInstanceTypes `json:"instanceTypes,omitempty"`
		LaunchSpecification *nodeGroupSpotOceanLaunchSpec    `json:"launchSpecification,omitempty"`
	}

	nodeGroupSpotOceanInstanceTypes struct {
		Whitelist []string `json:"whitelist,omitempty"`
		Blacklist []string `json:"blacklist,omitempty"`
	}

	nodeGroupSpotOceanLoadBalancer struct {
		Type *string `json:"type,omitempty"`
		Arn  *string `json:"arn,omitempty"`
		Name *string `json:"name,omitempty"`
	}

	nodeGroupSpotOceanTag struct {
		Key   interface{} `json:"tagKey,omitempty"`
		Value interface{} `json:"tagValue,omitempty"`
	}

	nodeGroupSpotOceanScheduling struct {
		ShutdownHours *nodeGroupSpotOceanSchedulingShutdownHours `json:"shutdownHours,omitempty"`
		Tasks         []*nodeGroupSpotOceanSchedulingTask        `json:"tasks,omitempty"`
	}

	nodeGroupSpotOceanSchedulingShutdownHours struct {
		IsEnabled   *bool    `json:"isEnabled,omitempty"`
		TimeWindows []string `json:"timeWindows,omitempty"`
	}

	nodeGroupSpotOceanSchedulingTask struct {
		IsEnabled      *bool   `json:"isEnabled,omitempty"`
		Type           *string `json:"taskType,omitempty"`
		CronExpression *string `json:"cronExpression,omitempty"`
	}

	nodeGroupSpotOceanAutoScaler struct {
		IsEnabled    *bool                                   `json:"isEnabled,omitempty"`
		IsAutoConfig *bool                                   `json:"isAutoConfig,omitempty"`
		Cooldown     *int                                    `json:"cooldown,omitempty"`
		Headroom     *nodeGroupSpotOceanAutoScalerHeadroom   `json:"headroom,omitempty"`  // cluster
		Headrooms    []*nodeGroupSpotOceanAutoScalerHeadroom `json:"headrooms,omitempty"` // launchspec
	}

	nodeGroupSpotOceanAutoScalerHeadroom struct {
		CPUPerUnit    *int `json:"cpuPerUnit,omitempty"`
		GPUPerUnit    *int `json:"gpuPerUnit,omitempty"`
		MemoryPerUnit *int `json:"memoryPerUnit,omitempty"`
		NumOfUnits    *int `json:"numOfUnits,omitempty"`
	}

	nodeGroupSpotOceanLabel struct {
		Key   *string `json:"key,omitempty"`
		Value *string `json:"value,omitempty"`
	}

	nodeGroupSpotOceanTaint struct {
		Key    *string `json:"key,omitempty"`
		Value  *string `json:"value,omitempty"`
		Effect *string `json:"effect,omitempty"`
	}
)

// MarshalJSON implements the json.Marshaler interface.
func (x *nodeGroupSpotOceanResource) MarshalJSON() ([]byte, error) {
	var typ string
	if x.OceanCluster != nil {
		typ = "Custom::ocean"
	} else if x.OceanLaunchSpec != nil {
		typ = "Custom::oceanLaunchSpec"
	}
	type Properties nodeGroupSpotOceanResource
	return json.Marshal(&struct {
		Type       string
		Properties Properties
	}{
		Type:       typ,
		Properties: Properties(*x),
	})
}

// CloudFormationResource converts to AWS Cloud Formation resource.
func (x *nodeGroupSpotOceanResource) CloudFormationResource() (*awsCloudFormationResource, error) {
	b, err := json.Marshal(x)
	if err != nil {
		return nil, err
	}

	v := new(awsCloudFormationResource)
	if err := json.Unmarshal(b, v); err != nil {
		return nil, err
	}

	return v, nil
}

// newNodeGroupSpotOceanResource returns a Spot Ocean resource.
func (n *NodeGroupResourceSet) newNodeGroupSpotOceanResource(launchTemplate *gfn.AWSEC2LaunchTemplate,
	vpcZoneIdentifier interface{}, tags []map[string]interface{}) (*awsCloudFormationResource, error) {

	var resource *nodeGroupSpotOceanResource
	var err error

	// Resource.
	{
		if n.spec.Name == api.SpotOceanNodeGroupName {
			logger.Debug("spot: creating nodegroup as ocean cluster")
			resource, err = n.newNodeGroupSpotOceanClusterResource(
				launchTemplate, vpcZoneIdentifier, tags)
		} else {
			logger.Debug("spot: creating nodegroup as ocean launchspec")
			resource, err = n.newNodeGroupSpotOceanLaunchSpecResource(
				launchTemplate, vpcZoneIdentifier, tags)
		}
		if err != nil {
			return nil, err
		}
	}

	// Properties.
	{
		config := spotinst.DefaultConfig()
		config.WithCredentials(credentials.NewChainCredentials([]credentials.Provider{
			&credentials.EnvProvider{},
			&credentials.FileProvider{Profile: spotinst.StringValue(n.spec.SpotOcean.Metadata.Profile)},
		}...))

		c, err := config.Credentials.Get()
		if err != nil {
			return nil, err
		}

		resource.Token = spotinst.String(c.Token)
		resource.Account = spotinst.String(c.Account)
		resource.ServiceToken = gfn.MakeFnSubString("arn:aws:lambda:${AWS::Region}:178579023202:function:spotinst-cloudformation")
	}

	return resource.CloudFormationResource()
}

// newNodeGroupSpotOceanClusterResource returns a Spot Ocean cluster resource.
func (n *NodeGroupResourceSet) newNodeGroupSpotOceanClusterResource(launchTemplate *gfn.AWSEC2LaunchTemplate,
	vpcZoneIdentifier interface{}, tags []map[string]interface{}) (*nodeGroupSpotOceanResource, error) {

	template := launchTemplate.LaunchTemplateData
	cluster := &nodeGroupSpotOceanCluster{
		Name:      spotinst.String(n.clusterSpec.Metadata.Name),
		ClusterID: spotinst.String(n.clusterSpec.Metadata.Name),
		Region:    gfn.MakeRef("AWS::Region"),
		Compute: &nodeGroupSpotOceanCompute{
			LaunchSpecification: &nodeGroupSpotOceanLaunchSpec{
				ImageID:      template.ImageId,
				UserData:     template.UserData,
				KeyPair:      template.KeyName,
				EBSOptimized: n.spec.EBSOptimized,
			},
			SubnetIDs: vpcZoneIdentifier,
		},
		Capacity: &nodeGroupSpotOceanCapacity{
			Target:  n.spec.DesiredCapacity,
			Minimum: n.spec.MinSize,
			Maximum: n.spec.MaxSize,
		},
	}

	// Storage.
	{
		if n.spec.VolumeSize != nil && spotinst.IntValue(n.spec.VolumeSize) > 0 {
			cluster.Compute.LaunchSpecification.VolumeSize = n.spec.VolumeSize
		}
	}

	// Strategy.
	{
		if strategy := n.spec.SpotOcean.Strategy; strategy != nil {
			cluster.Strategy = &nodeGroupSpotOceanStrategy{
				SpotPercentage:           strategy.SpotPercentage,
				UtilizeReservedInstances: strategy.UtilizeReservedInstances,
				FallbackToOnDemand:       strategy.FallbackToOnDemand,
			}
		}
	}

	// IAM.
	{
		if template.IamInstanceProfile != nil {
			cluster.Compute.LaunchSpecification.IAMInstanceProfile = map[string]*gfn.Value{
				"arn": template.IamInstanceProfile.Arn,
			}
		}
	}

	// Networking.
	{
		if len(template.NetworkInterfaces) > 0 {
			if template.NetworkInterfaces[0].AssociatePublicIpAddress != nil {
				cluster.Compute.LaunchSpecification.AssociatePublicIPAddress = template.NetworkInterfaces[0].AssociatePublicIpAddress
			}

			if len(template.NetworkInterfaces[0].Groups) > 0 {
				cluster.Compute.LaunchSpecification.SecurityGroupIDs = template.NetworkInterfaces[0].Groups
			}
		}
	}

	// Load Balancers.
	{
		var lbs []*nodeGroupSpotOceanLoadBalancer

		// ALBs.
		if len(n.spec.TargetGroupARNs) > 0 {
			for _, arn := range n.spec.TargetGroupARNs {
				lbs = append(lbs, &nodeGroupSpotOceanLoadBalancer{
					Type: spotinst.String("TARGET_GROUP"),
					Arn:  spotinst.String(arn),
				})
			}
		}

		if len(lbs) > 0 {
			cluster.Compute.LaunchSpecification.LoadBalancers = lbs
		}
	}

	// Tags.
	{
		var tagsKV []*nodeGroupSpotOceanTag

		// Nodegroup tags.
		if len(n.spec.Tags) > 0 {
			for key, value := range n.spec.Tags {
				tagsKV = append(tagsKV, &nodeGroupSpotOceanTag{
					Key:   spotinst.String(key),
					Value: spotinst.String(value),
				})
			}
		}

		// Resource tags (Name, kubernetes.io/*, k8s.io/*, etc.).
		if len(tags) > 0 {
			for _, tag := range tags {
				tagsKV = append(tagsKV, &nodeGroupSpotOceanTag{
					Key:   tag["Key"],
					Value: tag["Value"],
				})
			}
		}

		// Shared tags (metadata.tags + eksctl's tags).
		if len(n.sharedTags) > 0 {
			for _, tag := range n.sharedTags {
				tagsKV = append(tagsKV, &nodeGroupSpotOceanTag{
					Key:   spotinst.StringValue(tag.Key),
					Value: spotinst.StringValue(tag.Value),
				})
			}
		}

		if len(tagsKV) > 0 {
			cluster.Compute.LaunchSpecification.Tags = tagsKV
		}
	}

	// Instance Types.
	{
		if compute := n.spec.SpotOcean.Compute; compute != nil && compute.InstanceTypes != nil {
			cluster.Compute.InstanceTypes = &nodeGroupSpotOceanInstanceTypes{
				Whitelist: compute.InstanceTypes.Whitelist,
				Blacklist: compute.InstanceTypes.Blacklist,
			}
		}
	}

	// Scheduling.
	{
		if scheduling := n.spec.SpotOcean.Scheduling; scheduling != nil {
			if hours := scheduling.ShutdownHours; hours != nil {
				cluster.Scheduling = &nodeGroupSpotOceanScheduling{
					ShutdownHours: &nodeGroupSpotOceanSchedulingShutdownHours{
						IsEnabled:   hours.IsEnabled,
						TimeWindows: hours.TimeWindows,
					},
				}
			}

			if tasks := scheduling.Tasks; len(tasks) > 0 {
				if cluster.Scheduling == nil {
					cluster.Scheduling = new(nodeGroupSpotOceanScheduling)
				}

				cluster.Scheduling.Tasks = make([]*nodeGroupSpotOceanSchedulingTask, len(tasks))
				for i, task := range tasks {
					cluster.Scheduling.Tasks[i] = &nodeGroupSpotOceanSchedulingTask{
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
			cluster.AutoScaler = &nodeGroupSpotOceanAutoScaler{
				IsEnabled:    autoScaler.Enabled,
				IsAutoConfig: autoScaler.AutoConfig,
				Cooldown:     autoScaler.Cooldown,
			}

			if headrooms := autoScaler.Headrooms; len(headrooms) > 0 {
				cluster.AutoScaler.Headroom = &nodeGroupSpotOceanAutoScalerHeadroom{
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
			gfn.MakeRef("NodeGroup"),
			true)
	}

	return &nodeGroupSpotOceanResource{
		OceanCluster: cluster,
		OceanSummary: &nodeGroupSpotOceanSummary{
			ImageID:  cluster.Compute.LaunchSpecification.ImageID,
			Capacity: cluster.Capacity,
		},
	}, nil
}

// newNodeGroupSpotOceanLaunchSpecResource returns a Spot Ocean launchspec resource.
func (n *NodeGroupResourceSet) newNodeGroupSpotOceanLaunchSpecResource(launchTemplate *gfn.AWSEC2LaunchTemplate,
	vpcZoneIdentifier interface{}, tags []map[string]interface{}) (*nodeGroupSpotOceanResource, error) {

	// Import the Ocean cluster identifier.
	oceanClusterStackName := fmt.Sprintf("eksctl-%s-nodegroup-ocean", n.clusterSpec.Metadata.Name)
	oceanClusterID := makeImportValue(oceanClusterStackName, outputs.NodeGroupSpotOceanClusterID)

	template := launchTemplate.LaunchTemplateData
	spec := &nodeGroupSpotOceanLaunchSpec{
		Name:      spotinst.String(n.spec.Name),
		OceanID:   oceanClusterID,
		ImageID:   template.ImageId,
		UserData:  template.UserData,
		SubnetIDs: vpcZoneIdentifier,
	}

	// Storage.
	{
		if n.spec.VolumeSize != nil && spotinst.IntValue(n.spec.VolumeSize) > 0 {
			spec.VolumeSize = n.spec.VolumeSize
		}
	}

	// IAM.
	{
		if template.IamInstanceProfile != nil {
			spec.IAMInstanceProfile = map[string]*gfn.Value{
				"arn": template.IamInstanceProfile.Arn,
			}
		}
	}

	// Networking.
	{
		if len(template.NetworkInterfaces) > 0 {
			if len(template.NetworkInterfaces[0].Groups) > 0 {
				spec.SecurityGroupIDs = template.NetworkInterfaces[0].Groups
			}
		}
	}

	// Tags.
	{
		var tagsKV []*nodeGroupSpotOceanTag

		// Nodegroup tags.
		if len(n.spec.Tags) > 0 {
			for key, value := range n.spec.Tags {
				tagsKV = append(tagsKV, &nodeGroupSpotOceanTag{
					Key:   spotinst.String(key),
					Value: spotinst.String(value),
				})
			}
		}

		// Resource tags (Name, kubernetes.io/*, k8s.io/*, etc.).
		if len(tags) > 0 {
			for _, tag := range tags {
				tagsKV = append(tagsKV, &nodeGroupSpotOceanTag{
					Key:   tag["Key"],
					Value: tag["Value"],
				})
			}
		}

		// Shared tags (metadata.tags + eksctl's tags).
		if len(n.sharedTags) > 0 {
			for _, tag := range n.sharedTags {
				tagsKV = append(tagsKV, &nodeGroupSpotOceanTag{
					Key:   spotinst.StringValue(tag.Key),
					Value: spotinst.StringValue(tag.Value),
				})
			}
		}

		if len(tagsKV) > 0 {
			spec.Tags = tagsKV
		}
	}

	// Labels.
	{
		if len(n.spec.Labels) > 0 {
			labels := make([]*nodeGroupSpotOceanLabel, 0, len(n.spec.Labels))

			for key, value := range n.spec.Labels {
				labels = append(labels, &nodeGroupSpotOceanLabel{
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
			taints := make([]*nodeGroupSpotOceanTaint, 0, len(n.spec.Taints))

			for key, valueEffect := range n.spec.Taints {
				taint := &nodeGroupSpotOceanTaint{
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
			headrooms := make([]*nodeGroupSpotOceanAutoScalerHeadroom, len(autoScaler.Headrooms))

			for i, headroom := range autoScaler.Headrooms {
				headrooms[i] = &nodeGroupSpotOceanAutoScalerHeadroom{
					CPUPerUnit:    headroom.CPUPerUnit,
					GPUPerUnit:    headroom.GPUPerUnit,
					MemoryPerUnit: headroom.MemoryPerUnit,
					NumOfUnits:    headroom.NumOfUnits,
				}
			}

			spec.AutoScaler = &nodeGroupSpotOceanAutoScaler{
				Headrooms: headrooms,
			}
		}
	}

	// Outputs.
	{
		n.rs.defineOutputWithoutCollector(
			outputs.NodeGroupSpotOceanLaunchSpecID,
			gfn.MakeRef("NodeGroup"),
			true)
	}

	return &nodeGroupSpotOceanResource{
		OceanLaunchSpec: spec,
		OceanSummary: &nodeGroupSpotOceanSummary{
			ImageID: spec.ImageID,
			Capacity: &nodeGroupSpotOceanCapacity{
				Target:  n.spec.DesiredCapacity,
				Minimum: n.spec.MinSize,
				Maximum: n.spec.MaxSize,
			},
		},
	}, nil
}
