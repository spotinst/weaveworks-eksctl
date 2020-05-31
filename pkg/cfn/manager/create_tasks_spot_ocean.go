package manager

import (
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/spot"
)

// NewTasksToCreateSpotOceanNodeGroup defines tasks required to create Spot
// Ocean cluster.
func (c *StackCollection) NewTasksToCreateSpotOceanNodeGroup(
	nodeGroups []*api.NodeGroup, supportsManagedNodes bool) (*TaskTree, error) {
	tasks := &TaskTree{Parallel: true}

	// Verify before proceeding.
	ng, _, err := spot.ShouldCreateOceanNodeGroup(nodeGroups)
	if err != nil {
		return nil, err
	}
	if ng == nil { // already exists
		return tasks, nil
	}

	// Allow post-creation actions to be performed on this nodegroup as well.
	c.spec.NodeGroups = append(c.spec.NodeGroups, ng)

	// Add a new creation task.
	tasks.Append(&nodeGroupTask{
		info:                 "spot: create ocean cluster",
		nodeGroup:            ng,
		stackCollection:      c,
		supportsManagedNodes: supportsManagedNodes,
	})

	return tasks, nil
}
