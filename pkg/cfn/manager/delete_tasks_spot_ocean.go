package manager

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/kris-nova/logger"
	"github.com/weaveworks/eksctl/pkg/cfn/outputs"
	"github.com/weaveworks/eksctl/pkg/spot"
)

// NewTasksToDeleteSpotOceanNodeGroup defines tasks required to delete Spot
// Ocean cluster.
func (c *StackCollection) NewTasksToDeleteSpotOceanNodeGroup(shouldDelete func(string) bool) (*TaskTree, error) {
	tasks := &TaskTree{Parallel: true}

	// Describe all nodegroup stacks.
	stacks, err := c.DescribeNodeGroupStacks()
	if err != nil {
		return nil, err
	}

	// Verify before proceeding.
	ng, _, err := spot.ShouldDeleteOceanNodeGroup(stacks, shouldDelete)
	if err != nil {
		return nil, err
	}
	if ng == nil { // does not exist
		return tasks, nil
	}

	// All nodegroups are marked for deletion. We have to wait for their deletion
	// to complete before proceeding for deletion of the Ocean cluster.
	deleter := func(s *Stack, errs chan error) error {
		attempt := 0
		maxAttempts := 10

		ignoreErr := func(errMsg string) bool {
			errMsgs := []string{
				"not imported by any stack",
				"does not exist",
			}
			for _, msg := range errMsgs {
				if strings.Contains(errMsg, msg) {
					return true
				}
			}
			return false
		}

		for {
			attempt++
			logger.Debug("spot: attempting to delete ocean "+
				"cluster (attempt: %d/%d)", attempt, maxAttempts)

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
				if !ignoreErr(awsErr.Message()) {
					return err
				}
			}

			if output != nil && len(output.Imports) > 0 {
				if attempt+1 > maxAttempts {
					return fmt.Errorf("spot: max attempts reached: " +
						"giving up waiting for importers to become deleted")
				}

				logger.Debug("spot: waiting for %d importers "+
					"to become deleted", len(output.Imports))
				time.Sleep(10 * time.Second)
				continue
			}

			logger.Debug("spot: no more active importers; deleting...")
			return c.DeleteStackBySpecSync(s, errs)
		}
	}

	// Add a new deletion task.
	tasks.Append(&taskWithStackSpec{
		info:  "spot: delete ocean cluster",
		stack: ng,
		call:  deleter,
	})

	return tasks, nil
}
