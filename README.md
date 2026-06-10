Forked version with some bugfixes and improvements.

# Fleeting plugin for VMware vSphere

This is a [fleeting plugin](https://gitlab.com/gitlab-org/fleeting/fleeting) for VMware vSphere environments. The vSphere plugin allows GitLab Runner to provision virtual machines from templates, enabling
CI/CD jobs to be executed on dynamically created instances in your vSphere
infrastructure.

> [!note]
> This is a community maintained plugin and is not officially supported by GitLab or VMware.

## Testing Status

This plugin has been:

- [x] Validated against the [govmomi vcsim](https://github.com/vmware/govmomi/blob/main/vcsim/README.md) simulator
- [x] Confirmed by community members in real vSphere environments

As the maintainer, I don't currently have direct access to vSphere infrastructure. Community testing and feedback are welcome to further improve reliability across different setups.

## Installation

This plugin follows the standard installation process for Fleeting plugins. See the [GitLab Fleeting documentation](https://docs.gitlab.com/runner/fleet_scaling/fleeting/#install-with-the-oci-registry-distribution) for complete installation and configuration instructions.

When configuring, use the following plugin reference:

```toml
plugin = "registry.gitlab.com/santhanuv/fleeting-plugin-vmware-vsphere:latest"
```

## Configuration

The plugin requires configuration for both the vSphere environment and VM connection details.

### Provider Configuration

| Parameter             | Type   | Required | Description                                                                   |
|-----------------------|--------|----------|-------------------------------------------------------------------------------|
| `vsphere_url`         | string | Yes      | URL of the vCenter server                                                     |
| `username`            | string | No       | Username to access the vCenter server                                         |
| `password`            | string | No       | Password to access the vCenter server                                         |
| `template`            | string | Yes      | Path to the VM template (full clone) or VM with snapshots (linked clone)      |
| `allow_insecure_connection` | bool   | Yes      | Whether to skip SSL certificate verification                                  |
| `name`                | string | Yes      | Identifier for the instance group, used as prefix for VM names                |
| `clone_type`          | string | Yes      | Type of clone to make (`full`,`linked` or `instant`)                          |
| `snapshot`            | string | No       | Snapshot name for `linked` clones. If omitted, the current snapshot is used   |
| `folder`              | string | No       | Destination folder where VMs will be created                                  |
| `datacenter`          | string | No       | Datacenter where VMs will be created                                          |
| `host`                | string | No       | Target ESXi host for the cloned VMs                                           |
| `datastore`           | string | No       | Datastore where the cloned VMs will be located                                |
| `resource_pool`       | string | No       | Resource pool to which cloned VMs will be added                               |
| `guest_reboot_after_clone` | bool   | No       | Guest OS reboot immediately after instant clone operation.                    |
| `guest_command_after_clone` | string | No       | Run a command on the guest using guest-tools after an instant clone operation. |
| `guest_username`      | string | No       | Username to authenticate running the guest command as                         |
| `guest_password`      | string | No       | Password to authenticate running the guest command as                         |
| `cloudinit_mutation_script` | string | No       | Command or script to call to edit the supplied cloud-init for a new VM        |
| `post_start_script`   | string | No       | Command or script to call after starting a new VM                             |
| `netinfo_script`      | string | No       | Command for script to call before returning valid connect info to the runner  |
| `pre_shutdown_script` | string | No       | Command or script to call before shutting down new VM                         |

If optional parameters are not specified, the plugin will attempt to use default values from the vSphere environment.

### Connector Configuration

The plugin uses the following defaults for VM connections:

| Parameter | Default Value |
|-----------|---------------|
| Username | `"fleeting"` |
| Protocol | `"ssh"` for Linux VMs |
| OS Detection | Auto-detected from VM, defaults to Linux if unknown |

Note: When using
[Docker Autoscaler](https://docs.gitlab.com/runner/executors/docker_autoscaler/),
to enable Runner Manager’s access to the Docker socket on the VM, the user must be part of the `docker` group.

### Hook Scripts

The hook script options allow customizing the startup and shutdown process by invoking local commands where the
Gitlab Runner process is executing. Scripts receive pre-populated environment variables with information relevant to
the current process. 
Common environment received by hook scripts:

| Environment Variable | Description                                                                               |
|----------------------|-------------------------------------------------------------------------------------------|
| `GOVC_URL`           | Valid connections settings to VSphere - includes the username and password if supplied. |
| `GOVC_INSECURE`      | Populated from `allow_insecure_connection`                                                |


#### `cloudinit_mutation_script`

Executed before a VM is cloned and passed a path to a temporary file containing the JSON encoded cloud-init data.
Any alterations made to the temporary file become the _real_ cloud-init data sent to the cloned VM.

It is possible to make changes here which will prevent the plugin connecting to the vM, so be careful.

| Environment Variable | Description                                                      |
|----------------------|------------------------------------------------------------------|
| `CLOUDINIT_PATH`     | Path to a temporary file containing the JSON encoded cloud init. |
| `TARGET_NAME`        | Name which will be assigned to the virtual machine being started |

#### `post_start_script`

Executed just before the VM is considered to be up: this will be called after and optional guest command and guest
reboot has occurred.

| Environment Variable | Description                                                                      |
|----------------------|----------------------------------------------------------------------------------|
| `GOVC_VM`            | Stringified managed object reference to the cloned VM e.g. `VirtualMachine:vm-79` |

#### `netinfo_script`

Executed when valid network status information is being returned from the plugin. This can be used to configure
other systems just before the Gitlab runner takes control of the VM.

| Environment Variable | Description                                                                                    |
|----------------------|------------------------------------------------------------------------------------------------|
| `GOVC_VM`            | Stringified managed object reference to the cloned VM e.g. `VirtualMachine:vm-79`              |
| `TARGET_NAME`        | Name which was assigned to the virtual machine (this is the name Gitlab Runner refers to it as) |
| `GUEST_IP`        | The internal IP address which will be returned to the Gitlab Runner                            |
| `GUEST_HOSTNAME`           | Hostname as reported by the VSphere guest info                                                 |

#### `pre_shutdown_script`

Executed while the VM is still running and just before it is powered off.

| Environment Variable | Description                                     |
|----------------------|-------------------------------------------------|
| `GOVC_VM`            | Stringified managed object reference to the cloned VM e.g. `VirtualMachine:vm-79` |

## Clone Strategies

### Full Clone (default)

By default, the plugin creates full clones from a vSphere template. This copies the entire disk, which is reliable but can be slow for large VMs.

### Linked Clone

Linked clones use a snapshot-based delta disk instead of copying the entire disk, resulting in significantly faster clone operations and reduced storage usage.

To use linked clones:

1. Create a VM in vSphere and configure it as desired
2. Take a snapshot (e.g., `Base_1`)
3. Configure the plugin:

```toml
[runners.autoscaler.plugin_config]
  template = "my-source-vm"
  linked_clone = true
  snapshot = "Base_1"  # optional: omit to use the current snapshot
```

> [!note]
> For linked clones, the `template` parameter must point to a VM with at least one snapshot.

### Instant Clone

Instant clones are liked full-clones but perform a clone of a running VM, resulting an effectively instant clone.

## VM Provisioning

### Linux VMs

- Provisioning credentials is supported via cloud-init
- The template VM must be configured with cloud-init
- User data with username and SSH public key is injected into cloned VMs

### Windows VMs

- Provisioning credentials is not supported for Windows VMs
- Use static credentials with username and password

## Contributing

Contributions to this plugin are welcome and appreciated. You can help in several ways:

- Reporting issues you encounter
- Providing feedback from testing in vSphere environments
- Submitting bug fixes and improving documentation
- Suggesting new features or capabilities

Please open an issue or merge request in this repository to contribute.

## Acknowledgements

Thanks to community members who helped test and improve this plugin:

- Olivier Sechet (@osechet) for testing the plugin and contributing bug fixes
- Mathieu Wolf (@mathieu.wolf) for adding linked clone support and validating it on real vSphere infrastructure
