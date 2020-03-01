package cmdutils

import (
	"github.com/spf13/pflag"
)

func AddSpotOceanCreateNodeGroupFlags(fs *pflag.FlagSet, spotProfile *string, spotOcean *bool) {
	fs.StringVar(spotProfile, "spot-profile", "", "credentials profile to use")
	fs.BoolVar(spotOcean, "spot-ocean", false, "create Ocean-managed nodegroup")
}
