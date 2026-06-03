package version

import (
	"strings"

	"gitlab.com/gitlab-org/fleeting/fleeting/plugin"
)

const Name = "fleeting-plugin-vmware-vsphere"
const Description = `Fleeting Plugin for VSphere (wrouesnel fork)`

var Version string = "0.0.0" //nolint:gochecknoglobals

var VersionInfo plugin.VersionInfo

func init() {
	version, rest, _ := strings.Cut(Version, "-")
	_, commitish, _ := strings.Cut(rest, "-")

	VersionInfo = plugin.VersionInfo{
		Name:      Name,
		Version:   version,
		Revision:  "HEAD",
		Reference: commitish,
		BuiltAt:   "now",
	}
}
