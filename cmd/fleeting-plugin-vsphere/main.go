package main

import (
	vsphere "github.com/wrouesnel/fleeting-plugin-vmware-vsphere"
	"github.com/wrouesnel/fleeting-plugin-vmware-vsphere/version"
	"gitlab.com/gitlab-org/fleeting/fleeting/plugin"
)

func main() {
	plugin.Main(&vsphere.InstanceGroup{}, version.VersionInfo)
}
