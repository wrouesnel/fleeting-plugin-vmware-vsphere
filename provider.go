package vsphere

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/hashicorp/go-hclog"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
	vsphereclient "gitlab.com/santhanuv/fleeting-plugin-vmware-vsphere/internal/vsphere-client"
)

const MaxInstances = 50

var _ provider.InstanceGroup = (*InstanceGroup)(nil)

var newClient = vsphereclient.NewClient

type InstanceGroup struct {
	VsphereUrl         string                  `json:"vsphere_url"`
	Username           string                  `json:"username"`
	Password           string                  `json:"password"`
	Template           string                  `json:"template"`
	Folder             string                  `json:"folder"`
	Datacenter         string                  `json:"datacenter"`
	Host               string                  `json:"host"`
	Datastore          string                  `json:"datastore"`
	ResourcePool       string                  `json:"resource_pool"`
	InsecureConnection bool                    `json:"allow_insecure_connection"`
	CloneType          vsphereclient.CloneType `json:"clone_type"`
	Snapshot           string                  `json:"snapshot"`
	Name               string                  `json:"name"`

	// TODO: add support for an optional "cloud-init mutation hook" so we can
	// modify cloud-init.
	// CloudInitMutationHook string `json:"cloud_init_mutation_hook"`

	// TODO: add support for pre-/post- action hook scripts which receive VM info.
	// This would be how we can support things like dynamic monitoring easily.
	//PreStartScript     string `json:"pre_start_script"`
	//PostStartScript    string `json:"post_start_script"`
	//PreShutdownScript  string `json:"pre_shutdown_script"`
	//PostShutdownScript string `json:"post_shutdown_script"`

	size     uint
	client   vsphereclient.Client
	settings provider.Settings
	log      hclog.Logger

	sshPubKey []byte
}

func (g *InstanceGroup) Init(ctx context.Context, logger hclog.Logger, settings provider.Settings) (provider.ProviderInfo, error) {
	var options []vsphereclient.ClientOption

	if g.Datacenter != "" {
		options = append(options, vsphereclient.WithDatacenter(g.Datacenter))
	}

	if g.Folder != "" {
		options = append(options, vsphereclient.WithFolder(g.Folder))
	}

	if g.Host != "" {
		options = append(options, vsphereclient.WithHost(g.Host))
	}

	if g.ResourcePool != "" {
		options = append(options, vsphereclient.WithPool(g.ResourcePool))
	}

	if g.Datastore != "" {
		options = append(options, vsphereclient.WithDatastore(g.Datastore))
	}

	// Name is required
	options = append(options, vsphereclient.WithVMNamePrefix(g.Name))

	switch g.CloneType {
	case vsphereclient.CloneTypeFull:
		// No option change needed for full clone.
	case vsphereclient.CloneTypeLinked:
		options = append(options, vsphereclient.WithLinkedClone(g.Snapshot))
	case vsphereclient.CloneTypeInstant:
		options = append(options, vsphereclient.WithInstantClone())
	default:
		return provider.ProviderInfo{}, fmt.Errorf("unhandled clone type %q", g.CloneType)
	}

	client, err := newClient(ctx, g.VsphereUrl, g.InsecureConnection, g.Template, g.Username, g.Password, options...)
	if err != nil {
		return provider.ProviderInfo{}, err
	}

	g.client = client

	g.log = logger.With("data Center", g.Datacenter, "folder", g.Folder, "template", g.Template)
	g.settings = settings

	providerInfo := provider.ProviderInfo{
		ID:        path.Join("vsphere", g.Name, g.Datacenter),
		MaxSize:   MaxInstances,
		Version:   Version.String(),
		BuildInfo: Version.BuildInfo(),
	}

	if g.settings.UseStaticCredentials {
		return providerInfo, ctx.Err()
	}

	if g.settings.OS == "" {
		g.settings.OS = "linux"

		guestOsId, err := g.client.GuestOs(ctx)
		if err != nil {
			return providerInfo, nil
		}

		if strings.Contains(guestOsId, "win") {
			g.settings.OS = "windows"
		} else if strings.Contains(guestOsId, "darwin") {
			g.settings.OS = "darwin"
		}
	}

	if g.settings.Protocol == "" && g.settings.OS != "windows" {
		g.settings.Protocol = provider.ProtocolSSH
	}

	if g.settings.Username == "" {
		g.settings.Username = "fleeting"
	}

	if g.settings.Key == nil {
		key, err := g.generateSshKey()
		if err != nil {
			return provider.ProviderInfo{}, nil
		}

		g.settings.Key = key
	}

	pubKey, err := g.getSshPubKey(g.settings.Key)
	if err != nil {
		return provider.ProviderInfo{}, err
	}

	g.sshPubKey = pubKey

	return providerInfo, ctx.Err()
}

func (g *InstanceGroup) ConnectInfo(ctx context.Context, id string) (provider.ConnectInfo, error) {
	info := provider.ConnectInfo{
		ID:              id,
		ConnectorConfig: g.settings.ConnectorConfig,
	}

	internalIP, err := g.client.NetInfo(ctx, id)
	if err != nil {
		return provider.ConnectInfo{}, fmt.Errorf("fetching ip address: %w", err)
	}
	info.InternalAddr = internalIP

	if info.UseStaticCredentials {
		return info, nil
	}

	if info.OS == "windows" {
		return provider.ConnectInfo{}, fmt.Errorf("provisioning credential for windows is not supported")
	}

	if info.Protocol == "" {
		info.Protocol = provider.ProtocolSSH
	}

	if info.Username == "" {
		info.Username = g.settings.Username
	}

	switch info.Protocol {
	case provider.ProtocolSSH:
		if info.Key == nil {
			info.Key = g.settings.Key
		}
	}

	return info, nil
}

func (g *InstanceGroup) Update(ctx context.Context, update func(instance string, state provider.State)) error {
	vms, err := g.client.GetVMs(ctx, g.log)
	if err != nil {
		return err
	}

	for name, state := range vms {
		update(name, state)
	}

	return nil
}

func (g *InstanceGroup) Increase(ctx context.Context, delta int) (int, error) {
	var guestopts *vsphereclient.GuestOsOpts
	if !g.settings.UseStaticCredentials {
		guestopts = &vsphereclient.GuestOsOpts{
			Username: g.settings.Username,
			PubKey:   g.sshPubKey,
		}
	}
	count, err := g.client.TemplateClone(ctx, uint(delta), g.log, guestopts)
	if err != nil {
		return 0, fmt.Errorf("cloning from template: %w", err)
	}

	g.size += count

	return int(count), nil
}

func (g *InstanceGroup) Decrease(ctx context.Context, instances []string) ([]string, error) {
	deleted, err := g.client.DeleteVMs(ctx, instances, g.log)
	if err != nil {
		return nil, err
	}

	count := int(g.size) - len(deleted)

	if count < 0 {
		g.log.Error("out-of-sync size", "count", count, "size", g.size, "deleted", len(deleted))
	}

	g.size = uint(count)

	return deleted, nil
}

// Heartbeat is typically called by the taskscaler before connecting to the instance.
// TODO: Implement check related to VM health state (e.g., power state, guest heartbeat).
//
// HINT: Too many API calls should be avoided, as ConnectInfo is called subsequently.
func (g *InstanceGroup) Heartbeat(ctx context.Context, id string) error {
	return nil
}

func (g *InstanceGroup) Suspend(ctx context.Context, instances []string) (succeeded []string, err error) {
	// TODO: investigate the benefits of implementing this interface beyond a stub
	return []string{}, nil
}

func (g *InstanceGroup) Resume(ctx context.Context, instances []string) (succeeded []string, err error) {
	// TODO: investigate the benefits of implementing this interface stub
	return []string{}, nil
}

func (g *InstanceGroup) Shutdown(ctx context.Context) error {
	remaining, err := g.client.GetVMs(ctx, g.log)
	if err != nil {
		return err
	}

	instances := make([]string, 0, len(remaining))
	for name := range remaining {
		instances = append(instances, name)
	}

	deleted, err := g.client.DeleteVMs(ctx, instances, g.log)
	if err != nil {
		return err
	}

	if len(deleted) == len(instances) {
		return nil
	}

	var notDeleted []string
	for _, name := range instances {
		if !slices.Contains(deleted, name) {
			notDeleted = append(notDeleted, name)
		}
	}

	return fmt.Errorf("failed to delete vm instances: %s", strings.Join(notDeleted, ","))
}
