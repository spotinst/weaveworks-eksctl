package cmdutils

import (
	"github.com/spf13/pflag"
)

func AddSpotOceanCommonFlags(fs *pflag.FlagSet, spotProfile *string) {
	fs.StringVar(spotProfile, "spot-profile", "", "credentials profile to use")
}

func AddSpotOceanCreateNodeGroupFlags(fs *pflag.FlagSet, spotOcean *bool) {
	fs.BoolVar(spotOcean, "spot-ocean", false, "create Ocean-managed nodegroup")
}
