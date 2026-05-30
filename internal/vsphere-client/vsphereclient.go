//go:generate go tool go-enum --marshal --names --values
package vsphereclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/hashicorp/go-hclog"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/task"
	"github.com/vmware/govmomi/vim25/json"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

type Client interface {
	DeleteVMs(ctx context.Context, vmNames []string, log hclog.Logger) ([]string, error)
	TemplateClone(ctx context.Context, count uint, log hclog.Logger, guestopts *GuestOsOpts) (uint, error)
	GetVMs(ctx context.Context, logger hclog.Logger) (map[string]provider.State, error)
	NetInfo(ctx context.Context, vmName string) (string, error)
	GuestOs(ctx context.Context) (string, error)
}

// CloneType specifies the type of clone to make
// ENUM(
// full,
// linked,
// instant,
// )
type CloneType string

type ClientOption func(ctx context.Context, c *client, finder *find.Finder) error

type client struct {
	client       *govmomi.Client
	datacenter   types.ManagedObjectReference
	pool         types.ManagedObjectReference
	host         *types.ManagedObjectReference
	datastore    types.ManagedObjectReference
	folder       types.ManagedObjectReference
	template     types.ManagedObjectReference
	namePrefix   string
	cloneType    CloneType
	snapshotName string
	snapshot     *types.ManagedObjectReference

	// snapshotMtx protects some internal variable modifications
	snapshotMtx *sync.Mutex
}

func NewClient(ctx context.Context, vsphereUrl string, insecure bool, template string, username string, password string, options ...ClientOption) (Client, error) {
	targetURL, err := parseURL(vsphereUrl, username, password)
	if err != nil {
		return nil, err
	}

	gc, err := govmomi.NewClient(ctx, targetURL, insecure)
	if err != nil {
		return nil, err
	}

	finder := find.NewFinder(gc.Client)

	c := client{
		client:      gc,
		cloneType:   CloneTypeFull, // default clone type
		snapshotMtx: new(sync.Mutex),
	}

	for _, option := range options {
		err := option(ctx, &c, finder)
		if err != nil {
			return nil, err
		}
	}

	if c.datacenter == (types.ManagedObjectReference{}) {
		dc, err := finder.DefaultDatacenter(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to setup default Datacenter: %w", err)
		}

		c.datacenter = dc.Reference()
	}

	finder.SetDatacenter(object.NewDatacenter(c.client.Client, c.datacenter))

	templateVM, err := finder.VirtualMachine(ctx, template)
	if err != nil {
		return nil, fmt.Errorf("failed to find source VM/template %s: %w", template, err)
	}

	isTemplate, err := templateVM.IsTemplate(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to confirm %s is a template: %w", template, err)
	}

	c.template = templateVM.Reference()

	switch c.cloneType {
	case CloneTypeFull:
		if !isTemplate {
			return nil, fmt.Errorf("%s should be a template", template)
		}
	case CloneTypeLinked:
		if isTemplate {
			return nil, fmt.Errorf("linked clone requires a VM with snapshots, but %s is a template", template)
		}
		// Initially resolve the template. If the template isn't found at VM launch time, this will be recalled
		// at that time.
		if err := c.resolveSnapshot(ctx, c.template); err != nil {
			return nil, err
		}
	case CloneTypeInstant:
		if isTemplate {
			return nil, fmt.Errorf("instant clone requires a running VM but %s is a template", template)
		}
		if powerState, err := templateVM.PowerState(ctx); err != nil {
			return nil, fmt.Errorf("failed to determine power state of %s VM: %w", template, err)
		} else if powerState != types.VirtualMachinePowerStatePoweredOn {
			// This is not a reliable check, but for most users it should catch that they're asking for a situation
			// that can't be accomodated. Obviously you could just power off the target VM at anytime.
			return nil, fmt.Errorf("instant clone requires a running VM but %s is a power state of %s", template, powerState)
		}
	default:
		return nil, fmt.Errorf("unknown clone type %s", c.cloneType)
	}

	if c.folder == (types.ManagedObjectReference{}) {
		folder, err := finder.DefaultFolder(ctx)
		if err != nil {
			return nil, err
		}

		c.folder = folder.Reference()
	}

	if c.pool == (types.ManagedObjectReference{}) {
		pool, err := finder.DefaultResourcePool(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to setup default Resource Pool: %w", err)
		}

		c.pool = pool.Reference()
	}

	if c.datastore == (types.ManagedObjectReference{}) {
		ds, err := finder.DefaultDatastore(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to setup default Datastore: %w", err)
		}

		c.datastore = ds.Reference()
	}

	return &c, nil
}

func WithFolder(folder string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		folder, err := finder.Folder(ctx, folder)
		if err != nil {
			return fmt.Errorf("failed to find folder %s: %w", folder, err)
		}

		c.folder = folder.Reference()
		return nil
	}
}

func WithDatacenter(datacenter string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		ds, err := finder.Datacenter(ctx, datacenter)
		if err != nil {
			return fmt.Errorf("failed to find the datacenter '%s': %w", datacenter, err)
		}

		c.datacenter = ds.Reference()
		return nil
	}
}

func WithPool(pool string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		pool, err := finder.ResourcePool(ctx, pool)
		if err != nil {
			return fmt.Errorf("failed to find the resource pool %s: %w", pool, err)
		}

		c.pool = pool.Reference()
		return nil
	}
}

func WithHost(host string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		hostSystem, err := finder.HostSystem(ctx, host)
		if err != nil {
			return fmt.Errorf("failed to find the host %s: %w", host, err)
		}

		ref := hostSystem.Reference()
		c.host = &ref
		return nil
	}
}

func WithDatastore(datastore string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		ds, err := finder.Datastore(ctx, datastore)
		if err != nil {
			return fmt.Errorf("failed to find the datastore %s: %w", datastore, err)
		}

		c.datastore = ds.Reference()
		return nil
	}
}

func WithVMNamePrefix(prefix string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		if len(prefix) == 0 {
			return errors.New("instance group name is required")
		}
		c.namePrefix = prefix
		return nil
	}
}

func WithLinkedClone(snapshotName string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		c.cloneType = CloneTypeLinked
		c.snapshotName = snapshotName
		return nil
	}
}

func WithInstantClone() ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		c.cloneType = CloneTypeInstant
		return nil
	}
}

type taskResult struct {
	name      string
	isSuccess bool
	err       error
}

type GuestOsOpts struct {
	Username string
	PubKey   []byte
}

func (c *client) TemplateClone(ctx context.Context, count uint, log hclog.Logger, guestopts *GuestOsOpts) (uint, error) {
	if c == nil {
		return 0, fmt.Errorf("client needs to be initialized before cloning")
	}

	var config *types.VirtualMachineConfigSpec
	if guestopts != nil {
		userOptions, err := c.encodeUserData(guestopts.Username, guestopts.PubKey)
		if err != nil {
			return 0, err
		}

		config = &types.VirtualMachineConfigSpec{
			// Cloud-init configurations for adding user for ssh
			ExtraConfig: userOptions,
		}
	}

	var wg sync.WaitGroup
	resultChan := make(chan taskResult, count)

	for range count {
		wg.Add(1)

		go func() {
			defer wg.Done()

			name, err := c.templateClone(ctx, c.template, config)
			resultChan <- taskResult{
				name:      name,
				isSuccess: err == nil,
				err:       err,
			}
		}()
	}

	wg.Wait()
	close(resultChan)

	var newClones uint
	for result := range resultChan {
		if result.isSuccess {
			newClones++
			continue
		}

		log.Error("failure in vm clone", "error", result.err, "name", result.name)
	}

	return newClones, nil
}

func (c *client) GetVMs(ctx context.Context, logger hclog.Logger) (map[string]provider.State, error) {
	folder := object.NewFolder(c.client.Client, c.folder)

	var folderProps mo.Folder
	folder.Properties(ctx, folder.Reference(), []string{"childEntity"}, &folderProps)

	vms := make(map[string]provider.State)
	for _, mor := range folderProps.ChildEntity {
		if mor.Type != "VirtualMachine" {
			continue
		}

		vm := object.NewVirtualMachine(c.client.Client, mor)

		name, err := vm.ObjectName(ctx)
		if err != nil {
			logger.Error("failed to get vm name", "error", err, "mor", vm.Reference())
			continue
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		if !strings.HasPrefix(name, c.namePrefix) {
			continue
		}

		state, err := c.getVMState(ctx, mor)
		if err != nil {
			logger.Error("failed to get vm state", "error", err, "name", name, "mor", vm.Reference())
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		vms[name] = state
	}

	return vms, nil
}

func (c *client) DeleteVMs(ctx context.Context, vmNames []string, log hclog.Logger) ([]string, error) {
	folder := object.NewFolder(c.client.Client, c.folder)

	var folderProps mo.Folder
	folder.Properties(ctx, folder.Reference(), []string{"childEntity"}, &folderProps)

	vms := make(map[string]types.ManagedObjectReference, len(vmNames))
	for _, mor := range folderProps.ChildEntity {
		if mor.Type != "VirtualMachine" {
			continue
		}

		vm := object.NewVirtualMachine(c.client.Client, mor)
		name, err := vm.ObjectName(ctx)
		if err != nil {
			continue
		}

		if slices.Contains(vmNames, name) {
			vms[name] = vm.Reference()
		}
	}

	for _, vmName := range vmNames {
		if _, ok := vms[vmName]; !ok {
			log.Error("failure in vm deletion", "error", "failed to find VM in the destFolder", "name", vmName)
		}
	}

	var wg sync.WaitGroup
	resultChan := make(chan taskResult, len(vms))

	for name, vm := range vms {
		wg.Add(1)

		go func(vm types.ManagedObjectReference, name string) {
			defer wg.Done()

			err := c.deleteVM(ctx, vm, name)
			resultChan <- taskResult{
				name:      name,
				isSuccess: err == nil,
				err:       err,
			}
		}(vm, name)
	}

	wg.Wait()
	close(resultChan)

	deletedVms := make([]string, 0, len(vms))
	for result := range resultChan {
		if result.isSuccess {
			deletedVms = append(deletedVms, result.name)
			continue
		}

		log.Error("failure in vm deletion", "error", result.err, "name", result.name)
	}

	return deletedVms, nil
}

func (c *client) templateClone(ctx context.Context, src types.ManagedObjectReference, config *types.VirtualMachineConfigSpec) (targetName string, err error) {
	// Rewrite task errors so they print more useful information by JSON marshalling them
	// (which exposes it).
	defer func() {
		if taskError, ok := errors.AsType[task.Error](err); ok {
			encoded, _ := json.Marshal(taskError)
			err = errors.Join(err, fmt.Errorf("task error: %v", string(encoded)))
		}
	}()

	if !c.cloneType.IsValid() {
		return "", fmt.Errorf("unknown clone type: %s", c.cloneType)
	}

	srcVM := object.NewVirtualMachine(c.client.Client, src)

	// Use UUIDv7 to get temporal sorting.
	var id uuid.UUID
	id, err = uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("error generating UUID: %w, err")
	}
	targetName = fmt.Sprintf("%s-%s", c.namePrefix, id)

	folder := object.NewFolder(c.client.Client, c.folder)

	var task *object.Task

	vmLocation := types.VirtualMachineRelocateSpec{
		Folder:    &c.folder,
		Pool:      &c.pool,
		Host:      c.host,
		Datastore: &c.datastore,
	}

	// Instant clones are special
	if c.cloneType == CloneTypeInstant {
		// TODO: should support cloning the VM into another network.
		// The instant clone needs to be made with a disabled network card otherwise it would
		// immediately just fail. Mark all network cards as disconnected on the clone.
		var devices object.VirtualDeviceList
		devices, err = srcVM.Device(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get device list of template VM: %w", err)
		}
		// prepare virtual device config spec for network card
		configSpecs := []types.BaseVirtualDeviceConfigSpec{}
		for _, device := range devices {
			if card, ok := device.(types.BaseVirtualEthernetCard); ok {
				op := types.VirtualDeviceConfigSpecOperationEdit

				// Disconnect the network device on the instant cloned VM.
				// We must reconnect it below so it can adopt a new MAC address.
				// For safety we also set MAC address assignment to automatic.
				veth := card.GetVirtualEthernetCard()
				veth.Connectable.MigrateConnect = string(types.VirtualDeviceConnectInfoMigrateConnectOpDisconnect)
				veth.AddressType = string(types.VirtualEthernetCardMacTypeGenerated)

				configSpecs = append(configSpecs, &types.VirtualDeviceConfigSpec{
					Operation: op,
					Device:    veth,
				})
			}
		}

		vmLocation.DeviceChange = configSpecs

		instantcloneSpec := &types.VirtualMachineInstantCloneSpec{
			Name:     targetName,
			Location: vmLocation,
			// This will simply be the cloud-init data. Theoretically we can in fact process that
			// in the clone, but at the very least making it available lets scripts and other tools
			// detect it.
			Config: config.ExtraConfig,
		}

		task, err = srcVM.InstantClone(ctx, *instantcloneSpec)
		if err != nil {
			return "", fmt.Errorf("failed to instant clone VM: %w", err)
		}
	} else {
		spec := types.VirtualMachineCloneSpec{
			Location: vmLocation,
			Config:   config,
			PowerOn:  false, // This field is ignored when cloning from a template
			Template: false,
		}

		if c.cloneType == CloneTypeLinked {
			spec.Snapshot = c.getSnapshot()
			spec.Location.DiskMoveType = string(types.VirtualMachineRelocateDiskMoveOptionsCreateNewChildDiskBacking)
		}

		// HACK: if we're a linked clone and we fail, we want to try re-resolving the snapshot before actually failing.
		// This is being done this way because I don't currently know exactly what the error looks like.
		secondChance := false
		for {
			task, err = srcVM.Clone(ctx, folder, targetName, spec)
			if err != nil {
				if spec.Snapshot != nil && !secondChance {
					secondChance = true
					if serr := c.resolveSnapshot(ctx, c.template); serr != nil {
						return "", fmt.Errorf("failed to clone VM from template: %w\nsnapshot re-resolution failed: %w", err, serr)
					}
					continue // Try a second time after re-resolving the snapshot
				}
				return "", fmt.Errorf("failed to clone VM from template: %w", err)
			}
			// Success - process.
			break
		}
	}

	err = task.Wait(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to wait for VM cloning to complete: %w", err)
	}

	var folderProps mo.Folder
	// TODO: check if a useful error appears here.
	folder.Properties(ctx, folder.Reference(), []string{"childEntity"}, &folderProps)

	var clonedVM *object.VirtualMachine
	for _, mor := range folderProps.ChildEntity {
		if mor.Type != "VirtualMachine" {
			continue
		}

		vm := object.NewVirtualMachine(c.client.Client, mor)
		name, err := vm.ObjectName(ctx)
		if err != nil {
			continue
		}

		if name == targetName {
			clonedVM = vm
			break
		}
	}

	if clonedVM == nil {
		return targetName, fmt.Errorf("failed to find the newly cloned VM '%s'", targetName)
	}

	// Got the cloned VM at this point. Defer deleting if we have an error...
	defer func() {
		if clonedVM != nil && err != nil {
			if derr := c.deleteVM(ctx, clonedVM.Reference(), targetName); derr != nil {
				// Return the delete error as a priority instead
				err = derr
			}
		}
	}()

	// Power on the VM unless it was an instant clone (in which case it's already running)
	switch c.cloneType {
	case CloneTypeInstant:
		// TODO: consider providing the option to just reboot the instant clone immediately.
		// TODO: consider moving this to it's own function
		// The instant clone is performed by disconnecting the guest network. We must re-enable it
		// here so the machine can re-DHCP. Whatever template is being used needs to handle this
		// situation properly.
		// Reference: https://techdocs.broadcom.com/us/en/vmware-cis/vsphere/vsphere-sdks-tools/8-0/web-services-sdk-programming-guide/virtual-machine-management/linked-virtual-machines/instant-clone-virtual-machines/avoiding-network-identity-collisions-after-instant-clone-operations.html
		var devices object.VirtualDeviceList
		devices, err = srcVM.Device(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get device list of cloned VM: '%v': %w", targetName, err)
		}

		// prepare virtual device config spec for network card
		configSpecs := []types.BaseVirtualDeviceConfigSpec{}
		for _, device := range devices {
			if card, ok := device.(types.BaseVirtualEthernetCard); ok {
				op := types.VirtualDeviceConfigSpecOperationEdit
				// Reconnect all network devices on the clone.
				veth := card.GetVirtualEthernetCard()
				veth.Connectable.Connected = true

				configSpecs = append(configSpecs, &types.VirtualDeviceConfigSpec{
					Operation: op,
					Device:    veth,
				})
			}
		}

		task, err = clonedVM.Relocate(ctx, types.VirtualMachineRelocateSpec{
			DeviceChange: configSpecs,
		}, types.VirtualMachineMovePriorityDefaultPriority)
		if err != nil {
			return targetName, fmt.Errorf("failed to relocate VM to re-enable network '%s': %w", targetName, err)
		}
		err = task.Wait(ctx)
		if err != nil {
			return targetName, fmt.Errorf("failed to wait for VM '%s' network configuration: %w", targetName, err)
		}
	default:
		if err := c.powerOnVM(ctx, clonedVM.Reference(), targetName); err != nil {
			return targetName, err
		}
	}

	return targetName, nil
}

// getSnapshot provides a common wrapper for synchronized access to the current snapshot
func (c *client) getSnapshot() *types.ManagedObjectReference {
	c.snapshotMtx.Lock()
	defer c.snapshotMtx.Unlock()

	return c.snapshot
}

// getSnapshot provides a common wrapper for synchronized access to the current snapshot
func (c *client) setSnapshot(snapshot *types.ManagedObjectReference) {
	c.snapshotMtx.Lock()
	defer c.snapshotMtx.Unlock()

	c.snapshot = snapshot
}

func (c *client) resolveSnapshot(ctx context.Context, vmRef types.ManagedObjectReference) error {
	vm := object.NewVirtualMachine(c.client.Client, vmRef)

	var vmMo mo.VirtualMachine
	err := vm.Properties(ctx, vm.Reference(), []string{"snapshot"}, &vmMo)
	if err != nil {
		return fmt.Errorf("failed to retrieve snapshot info: %w", err)
	}

	if vmMo.Snapshot == nil {
		return fmt.Errorf("linked clone requires the source VM to have at least one snapshot")
	}

	if c.snapshotName == "" {
		if vmMo.Snapshot.CurrentSnapshot == nil {
			return fmt.Errorf("no current snapshot found on source VM")
		}
		c.setSnapshot(vmMo.Snapshot.CurrentSnapshot)
		return nil
	}

	queue := vmMo.Snapshot.RootSnapshotList
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		if s.Name == c.snapshotName {
			ref := s.Snapshot
			c.setSnapshot(&ref)
			return nil
		}
		queue = append(queue, s.ChildSnapshotList...)
	}

	return fmt.Errorf("snapshot '%s' not found on source VM", c.snapshotName)
}

func (c *client) powerOnVM(ctx context.Context, vmMOR types.ManagedObjectReference, vmName string) error {
	vm := object.NewVirtualMachine(c.client.Client, vmMOR)

	task, err := vm.PowerOn(ctx)
	if err != nil {
		return fmt.Errorf("failed to start VM '%s': %w", vmName, err)
	}

	err = task.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for VM '%s' startup: %w", vmName, err)
	}

	return nil
}

func (c *client) powerOffVM(ctx context.Context, vmMOR types.ManagedObjectReference, vmName string) error {
	vm := object.NewVirtualMachine(c.client.Client, vmMOR)

	task, err := vm.PowerOff(ctx)
	if err != nil {
		return fmt.Errorf("failed to power off VM '%s': %w", vmName, err)
	}

	err = task.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for VM '%s' power off: %w", vmName, err)
	}

	return nil
}

func (c *client) deleteVM(ctx context.Context, vmMOR types.ManagedObjectReference, vmName string) error {
	vm := object.NewVirtualMachine(c.client.Client, vmMOR)

	state, err := vm.PowerState(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete VM '%s': %w", vmName, err)
	}

	if state == types.VirtualMachinePowerStatePoweredOn {
		if err := c.powerOffVM(ctx, vmMOR, vmName); err != nil {
			return fmt.Errorf("failed to delete VM '%s': %w", vmName, err)
		}
	}

	task, err := vm.Destroy(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete VM '%s'", vmName)
	}

	err = task.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for VM '%s' deletion", vmName)
	}

	return nil
}

func (c *client) getVMState(ctx context.Context, vmMOR types.ManagedObjectReference) (provider.State, error) {
	vm := object.NewVirtualMachine(c.client.Client, vmMOR)

	vmName, err := vm.ObjectName(ctx)
	if err != nil {
		return provider.StateDeleting, fmt.Errorf("failed to get vm name: %w", err)
	}

	var vmInfo mo.VirtualMachine
	err = vm.Properties(ctx, vm.Reference(), []string{
		"runtime.powerState",
		"guest.net",
	}, &vmInfo)
	if err != nil {
		return provider.StateDeleting, fmt.Errorf("failed to virtual machine information: %w", err)
	}

	if vmInfo.Runtime.PowerState != types.VirtualMachinePowerStatePoweredOn {
		return provider.StateDeleting, nil
	}

	if vmInfo.Guest == nil {
		return provider.StateDeleting, fmt.Errorf("no guest info found for VM '%s'", vmName)
	}

	for _, nic := range vmInfo.Guest.Net {
		if nic.MacAddress == "" || nic.IpConfig == nil {
			continue
		}

		for _, ip := range nic.IpAddress {
			if vmip := net.ParseIP(ip).String(); vmip == "nil" {
				continue
			}

			return provider.StateRunning, nil
		}
	}

	return provider.StateCreating, nil
}

func (c *client) NetInfo(ctx context.Context, vmName string) (string, error) {
	folder := object.NewFolder(c.client.Client, c.folder)

	var folderProps mo.Folder
	folder.Properties(ctx, folder.Reference(), []string{"childEntity"}, &folderProps)

	var vm *object.VirtualMachine
	for _, mor := range folderProps.ChildEntity {
		if mor.Type != "VirtualMachine" {
			continue
		}

		v := object.NewVirtualMachine(c.client.Client, mor)
		name, err := v.ObjectName(ctx)
		if err != nil {
			continue
		}

		if name != vmName {
			continue
		}

		vm = v
		break
	}

	if vm == nil {
		return "", fmt.Errorf("failed to find vm '%s'", vmName)
	}

	var vmNetInfo mo.VirtualMachine
	err := vm.Properties(ctx, vm.Reference(), []string{"guest.net"}, &vmNetInfo)
	if err != nil {
		return "", err
	}

	if vmNetInfo.Guest == nil || vmNetInfo.Guest.Net == nil {
		return "", fmt.Errorf("failed to fetch the vm guest os net info")
	}

	var internalIP string
	for _, nic := range vmNetInfo.Guest.Net {
		if nic.MacAddress == "" || nic.IpConfig == nil {
			continue
		}

		for _, ip := range nic.IpAddress {
			if vmip := net.ParseIP(ip).String(); vmip == "nil" {
				continue
			} else {
				internalIP = vmip
				break
			}
		}
	}

	if internalIP == "" {
		return "", fmt.Errorf("failed to get ip address of vm '%s'", vmName)
	}

	return internalIP, nil
}

func (c *client) GuestOs(ctx context.Context) (string, error) {
	vm := object.NewVirtualMachine(c.client.Client, c.template)
	pc := property.DefaultCollector(c.client.Client)

	var vmMo mo.VirtualMachine
	err := pc.RetrieveOne(ctx, vm.Reference(), []string{"config.guestId"}, &vmMo)
	if err != nil {
		return "", err
	}

	if vmMo.Config == nil {
		return "", fmt.Errorf("failed to retrieve guest os info")
	}

	return vmMo.Config.GuestId, nil
}

// parseURL parses the given URL and combines the result with the given username and password.
// It ensures the username and password are encoded in a url-safe way.
func parseURL(vsphereUrl string, username string, password string) (*url.URL, error) {
	serverURL, err := url.Parse(vsphereUrl)
	if err != nil {
		return nil, err
	}

	if len(username) > 0 && len(password) > 0 {
		serverURL.User = url.UserPassword(username, password)
	}
	return serverURL, nil
}
