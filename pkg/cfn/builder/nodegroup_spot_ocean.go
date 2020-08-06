package builder

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kris-nova/logger"
	"github.com/spotinst/spotinst-sdk-go/spotinst"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/cfn/outputs"
	"github.com/weaveworks/eksctl/pkg/spot"
	gfn "github.com/weaveworks/goformation/cloudformation"
)

// newNodeGroupSpotOceanResource returns a Spot Ocean resource.
func (n *NodeGroupResourceSet) newNodeGroupSpotOceanResource(launchTemplate *gfn.AWSEC2LaunchTemplate,
	vpcZoneIdentifier interface{}, tags []map[string]interface{}) (*awsCloudFormationResource, error) {

	var res *spot.NodeGroupResource
	var out awsCloudFormationResource
	var err error

	type Parameter struct {
		Type                  string   `json:"Type"`
		Description           string   `json:"Description,omitempty"`
		Default               string   `json:"Default,omitempty"`
		AllowedPattern        string   `json:"AllowedPattern,omitempty"`
		AllowedValues         []string `json:"AllowedValues,omitempty"`
		ConstraintDescription string   `json:"ConstraintDescription,omitempty"`
		MaxLength             int      `json:"MaxLength,omitempty"`
		MinLength             int      `json:"MinLength,omitempty"`
		MaxValue              float64  `json:"MaxValue,omitempty"`
		MinValue              float64  `json:"MinValue,omitempty"`
		NoEcho                bool     `json:"NoEcho,omitempty"`
	}

	// Resource.
	{
		if n.spec.Name == api.SpotOceanNodeGroupName {
			logger.Debug("spot: creating ocean cluster for nodegroup %q", n.spec.Name)
			res, err = n.newNodeGroupSpotOceanClusterResource(
				launchTemplate, vpcZoneIdentifier, tags)
		} else {
			logger.Debug("spot: creating ocean launchspec for nodegroup %q", n.spec.Name)
			res, err = n.newNodeGroupSpotOceanLaunchSpecResource(
				launchTemplate, vpcZoneIdentifier, tags)
		}
		if err != nil {
			return nil, err
		}
	}

	// Credentials.
	{
		var profile *string
		if n.spec.SpotOcean.Metadata != nil {
			profile = n.spec.SpotOcean.Metadata.Profile
		}

		token, account, err := spot.LoadCredentials(profile)
		if err != nil {
			return nil, err
		}
		if token != "" {
			res.Token = n.rs.newParameter(spot.CredentialsTokenParameterKey, &Parameter{
				Type:    "String",
				Default: token,
			})
		}
		if account != "" {
			res.Account = n.rs.newParameter(spot.CredentialsAccountParameterKey, &Parameter{
				Type:    "String",
				Default: account,
			})
		}
	}

	// Feature Flags.
	{
		ff := spot.LoadFeatureFlags()
		if ff != "" {
			res.FeatureFlags = n.rs.newParameter(spot.FeatureFlagsParameterKey, &Parameter{
				Type:    "String",
				Default: ff,
			})
		}
	}

	// Service Token.
	{
		svc, err := spot.LoadServiceToken()
		if err != nil {
			return nil, err
		}
		if svc != "" {
			res.ServiceToken = gfn.MakeFnSubString(svc)
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
func (n *NodeGroupResourceSet) newNodeGroupSpotOceanClusterResource(launchTemplate *gfn.AWSEC2LaunchTemplate,
	vpcZoneIdentifier interface{}, tags []map[string]interface{}) (*spot.NodeGroupResource, error) {

	template := launchTemplate.LaunchTemplateData
	cluster := &spot.NodeGroupCluster{
		Name:      spotinst.String(n.clusterSpec.Metadata.Name),
		ClusterID: spotinst.String(n.clusterSpec.Metadata.Name),
		Region:    gfn.MakeRef("AWS::Region"),
		Compute: &spot.NodeGroupCompute{
			LaunchSpecification: &spot.NodeGroupLaunchSpec{
				ImageID:      template.ImageId,
				UserData:     template.UserData,
				KeyPair:      template.KeyName,
				EBSOptimized: n.spec.EBSOptimized,
			},
			SubnetIDs: vpcZoneIdentifier,
		},
		Capacity: &spot.NodeGroupCapacity{
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
			cluster.Strategy = &spot.NodeGroupStrategy{
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

			if template.NetworkInterfaces[0].Groups != nil {
				cluster.Compute.LaunchSpecification.SecurityGroupIDs = template.NetworkInterfaces[0].Groups
			}
		}
	}

	// Load Balancers.
	{
		var lbs []*spot.NodeGroupLoadBalancer

		// ELBs.
		if len(n.spec.ClassicLoadBalancerNames) > 0 {
			for _, name := range n.spec.ClassicLoadBalancerNames {
				lbs = append(lbs, &spot.NodeGroupLoadBalancer{
					Type: spotinst.String("CLASSIC"),
					Name: spotinst.String(name),
				})
			}
		}

		// ALBs.
		if len(n.spec.TargetGroupARNs) > 0 {
			for _, arn := range n.spec.TargetGroupARNs {
				lbs = append(lbs, &spot.NodeGroupLoadBalancer{
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
		var tagsKV []*spot.NodeGroupTag

		// Nodegroup tags.
		if len(n.spec.Tags) > 0 {
			for key, value := range n.spec.Tags {
				tagsKV = append(tagsKV, &spot.NodeGroupTag{
					Key:   spotinst.String(key),
					Value: spotinst.String(value),
				})
			}
		}

		// Resource tags (Name, kubernetes.io/*, k8s.io/*, etc.).
		if len(tags) > 0 {
			for _, tag := range tags {
				tagsKV = append(tagsKV, &spot.NodeGroupTag{
					Key:   tag["Key"],
					Value: tag["Value"],
				})
			}
		}

		// Shared tags (metadata.tags + eksctl's tags).
		if len(n.sharedTags) > 0 {
			for _, tag := range n.sharedTags {
				tagsKV = append(tagsKV, &spot.NodeGroupTag{
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
			cluster.Compute.InstanceTypes = &spot.NodeGroupInstanceTypes{
				Whitelist: compute.InstanceTypes.Whitelist,
				Blacklist: compute.InstanceTypes.Blacklist,
			}
		}
	}

	// Scheduling.
	{
		if scheduling := n.spec.SpotOcean.Scheduling; scheduling != nil {
			if hours := scheduling.ShutdownHours; hours != nil {
				cluster.Scheduling = &spot.NodeGroupScheduling{
					ShutdownHours: &spot.NodeGroupSchedulingShutdownHours{
						IsEnabled:   hours.IsEnabled,
						TimeWindows: hours.TimeWindows,
					},
				}
			}

			if tasks := scheduling.Tasks; len(tasks) > 0 {
				if cluster.Scheduling == nil {
					cluster.Scheduling = new(spot.NodeGroupScheduling)
				}

				cluster.Scheduling.Tasks = make([]*spot.NodeGroupSchedulingTask, len(tasks))
				for i, task := range tasks {
					cluster.Scheduling.Tasks[i] = &spot.NodeGroupSchedulingTask{
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
			cluster.AutoScaler = &spot.NodeGroupAutoScaler{
				IsEnabled:    autoScaler.Enabled,
				IsAutoConfig: autoScaler.AutoConfig,
				Cooldown:     autoScaler.Cooldown,
			}

			if headrooms := autoScaler.Headrooms; len(headrooms) > 0 {
				cluster.AutoScaler.Headroom = &spot.NodeGroupAutoScalerHeadroom{
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

	return &spot.NodeGroupResource{
		OceanCluster: cluster,
		OceanSummary: &spot.NodeGroupSummary{
			ImageID:  cluster.Compute.LaunchSpecification.ImageID,
			Capacity: cluster.Capacity,
		},
	}, nil
}

// newNodeGroupSpotOceanLaunchSpecResource returns a Spot Ocean launchspec resource.
func (n *NodeGroupResourceSet) newNodeGroupSpotOceanLaunchSpecResource(launchTemplate *gfn.AWSEC2LaunchTemplate,
	vpcZoneIdentifier interface{}, tags []map[string]interface{}) (*spot.NodeGroupResource, error) {

	// Import the Ocean cluster identifier.
	oceanClusterStackName := fmt.Sprintf("eksctl-%s-nodegroup-ocean", n.clusterSpec.Metadata.Name)
	oceanClusterID := makeImportValue(oceanClusterStackName, outputs.NodeGroupSpotOceanClusterID)

	template := launchTemplate.LaunchTemplateData
	spec := &spot.NodeGroupLaunchSpec{
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
			if template.NetworkInterfaces[0].Groups != nil {
				spec.SecurityGroupIDs = template.NetworkInterfaces[0].Groups
			}
		}
	}

	// Tags.
	{
		var tagsKV []*spot.NodeGroupTag

		// Nodegroup tags.
		if len(n.spec.Tags) > 0 {
			for key, value := range n.spec.Tags {
				tagsKV = append(tagsKV, &spot.NodeGroupTag{
					Key:   spotinst.String(key),
					Value: spotinst.String(value),
				})
			}
		}

		// Resource tags (Name, kubernetes.io/*, k8s.io/*, etc.).
		if len(tags) > 0 {
			for _, tag := range tags {
				tagsKV = append(tagsKV, &spot.NodeGroupTag{
					Key:   tag["Key"],
					Value: tag["Value"],
				})
			}
		}

		// Shared tags (metadata.tags + eksctl's tags).
		if len(n.sharedTags) > 0 {
			for _, tag := range n.sharedTags {
				tagsKV = append(tagsKV, &spot.NodeGroupTag{
					Key:   spotinst.StringValue(tag.Key),
					Value: spotinst.StringValue(tag.Value),
				})
			}
		}

		if len(tagsKV) > 0 {
			spec.Tags = tagsKV
		}
	}

	// Instance Types.
	{
		if compute := n.spec.SpotOcean.Compute; compute != nil && compute.InstanceTypes != nil {
			spec.InstanceTypes = compute.InstanceTypes.Whitelist
		}
	}

	// Labels.
	{
		if len(n.spec.Labels) > 0 {
			labels := make([]*spot.NodeGroupLabel, 0, len(n.spec.Labels))

			for key, value := range n.spec.Labels {
				labels = append(labels, &spot.NodeGroupLabel{
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
			taints := make([]*spot.NodeGroupTaint, 0, len(n.spec.Taints))

			for key, valueEffect := range n.spec.Taints {
				taint := &spot.NodeGroupTaint{
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
			headrooms := make([]*spot.NodeGroupAutoScalerHeadroom, len(autoScaler.Headrooms))

			for i, headroom := range autoScaler.Headrooms {
				headrooms[i] = &spot.NodeGroupAutoScalerHeadroom{
					CPUPerUnit:    headroom.CPUPerUnit,
					GPUPerUnit:    headroom.GPUPerUnit,
					MemoryPerUnit: headroom.MemoryPerUnit,
					NumOfUnits:    headroom.NumOfUnits,
				}
			}

			spec.AutoScaler = &spot.NodeGroupAutoScaler{
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

	return &spot.NodeGroupResource{
		OceanLaunchSpec: spec,
		OceanSummary: &spot.NodeGroupSummary{
			ImageID: spec.ImageID,
			Capacity: &spot.NodeGroupCapacity{
				Target:  n.spec.DesiredCapacity,
				Minimum: n.spec.MinSize,
				Maximum: n.spec.MaxSize,
			},
		},
	}, nil
}
