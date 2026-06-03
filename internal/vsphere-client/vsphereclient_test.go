package vsphereclient

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
	"github.com/wrouesnel/fleeting-plugin-vmware-vsphere/pkg/util"
)

func Test_parseURL(t *testing.T) {
	tests := []struct {
		name         string
		vsphereUrl   string
		username     string
		password     string
		wantUser     bool
		wantUsername string
		wantPassword string
		wantErr      bool
	}{
		{
			name:       "no username or password",
			vsphereUrl: "https://example.com/sdk",
			username:   "",
			password:   "",
			wantUser:   false,
			wantErr:    false,
		},
		{
			name:         "username and password in original url",
			vsphereUrl:   "https://origuser:origpass@example.com/sdk",
			username:     "",
			password:     "",
			wantUser:     true,
			wantUsername: "origuser",
			wantPassword: "origpass",
			wantErr:      false,
		},
		{
			name:         "overriden username and password in original url",
			vsphereUrl:   "https://origuser:origpass@example.com/sdk",
			username:     "user",
			password:     "pass",
			wantUser:     true,
			wantUsername: "user",
			wantPassword: "pass",
			wantErr:      false,
		},
		{
			name:         "with username and password",
			vsphereUrl:   "https://example.com/sdk",
			username:     "user",
			password:     "pass",
			wantUser:     true,
			wantUsername: "user",
			wantPassword: "pass",
			wantErr:      false,
		},
		{
			name:         "with special chars in username and password",
			vsphereUrl:   "https://example.com/sdk",
			username:     "user@domain.com",
			password:     "p@ss/w:rd",
			wantUser:     true,
			wantUsername: "user@domain.com",
			wantPassword: "p@ss/w:rd",
			wantErr:      false,
		},
		{
			name:       "username but empty password",
			vsphereUrl: "https://example.com/sdk",
			username:   "user",
			password:   "",
			wantUser:   false,
			wantErr:    false,
		},
		{
			name:       "invalid url",
			vsphereUrl: "://bad_url",
			username:   "user",
			password:   "pass",
			wantUser:   false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseURL(tt.vsphereUrl, tt.username, tt.password)
			if tt.wantErr {
				require.Error(t, err, "expected error")
				return
			}
			require.NoError(t, err, "unexpected error")
			if tt.wantUser {
				require.NotNil(t, got.User, "expected user info")
				username := got.User.Username()
				password, _ := got.User.Password()
				require.Equal(t, tt.wantUsername, username, "username mismatch")
				require.Equal(t, tt.wantPassword, password, "password mismatch")
			} else {
				require.Nil(t, got.User, "expected no user info")
			}
			// Ensure the returned URL parses as expected
			_, err = url.Parse(got.String())
			require.NoError(t, err, "returned URL is not valid")
		})
	}
}

const (
	datacenter     = "/DC0"
	host           = "/DC0/host/DC0_H0/DC0_H0"
	pool           = "/DC0/host/DC0_H0/Resources"
	datastore      = "/DC0/datastore/LocalDS_0"
	vmFolder       = "/DC0/vm"
	destFolderName = "fleeting-test"
	templateName   = "test-template"
	vmPathName     = "[LocalDS_0] fleeting-test"
)

// setupTestEnv duplicates the integration tests so we can check specific aspects
// of the vsphere client's functionality.
func setupTestEnv(t *testing.T, url *url.URL) error {
	t.Helper()
	ctx := context.Background()

	c, err := govmomi.NewClient(ctx, url, true)
	if err != nil {
		return err
	}

	finder := find.NewFinder(c.Client)

	dc, err := finder.Datacenter(ctx, datacenter)
	if err != nil {
		return err
	}
	finder.SetDatacenter(dc)

	f, err := finder.Folder(ctx, vmFolder)
	if err != nil {
		return err
	}

	vf, err := f.CreateFolder(ctx, destFolderName)
	if err != nil {
		return err
	}

	pool, err := finder.ResourcePool(ctx, pool)
	if err != nil {
		return err
	}

	host, err := finder.HostSystem(ctx, host)
	if err != nil {
		return err
	}

	spec := types.VirtualMachineConfigSpec{
		Name: templateName,
		Files: &types.VirtualMachineFileInfo{
			VmPathName: vmPathName,
		},
	}

	task, err := vf.CreateVM(ctx, spec, pool, host)
	if err != nil {
		return err
	}

	info, err := task.WaitForResult(ctx)
	if err != nil {
		return err
	}

	vm := object.NewVirtualMachine(c.Client, info.Result.(types.ManagedObjectReference))
	err = vm.MarkAsTemplate(ctx)
	if err != nil {
		return err
	}

	return nil
}

// TesttemplateClone tests the internal templateClone fucntion
func Test_templateClone_Basic(t *testing.T) {
	model := simulator.VPX()
	defer model.Remove()

	model.Datacenter = 1
	model.Host = 1
	model.Datastore = 1
	model.Cluster = 1
	model.Pool = 1
	model.Folder = 0

	err := model.Create()
	if err != nil {
		t.Fatalf("simulating vsphere: %s", err)
	}

	s := model.Service.NewServer()
	defer s.Close()

	err = setupTestEnv(t, s.URL)
	require.NoError(t, err)

	c, err := NewClient(
		t.Context(),
		s.URL.String(),
		true,
		templateName,
		"",
		"",
		WithPool(pool),
	)
	require.NoError(t, err)

	// Get the bare client
	bareClient := c.(*client)

	pKey, err := util.GenerateSshKey()
	require.NoError(t, err)
	pubKey, err := util.GetSshPubKey(pKey)
	require.NoError(t, err)

	targetName, err := bareClient.templateClone(t.Context(), hclog.NewNullLogger(), bareClient.template, &GuestOsOpts{
		Username: "test-user",
		PubKey:   pubKey,
	})
	require.NoError(t, err)
	require.Equal(t, true, targetName != "", "targetName was empty")
}

// TesttemplateClone tests the internal templateClone function
func Test_templateClone_WithCloudInitHook(t *testing.T) {
	model := simulator.VPX()
	defer model.Remove()

	model.Datacenter = 1
	model.Host = 1
	model.Datastore = 1
	model.Cluster = 1
	model.Pool = 1
	model.Folder = 0

	err := model.Create()
	if err != nil {
		t.Fatalf("simulating vsphere: %s", err)
	}

	s := model.Service.NewServer()
	defer s.Close()

	err = setupTestEnv(t, s.URL)
	require.NoError(t, err)

	_, scriptPath, scriptOutputPath := setupSuccessfulCloudInitHookScript(t)

	c, err := NewClient(
		t.Context(),
		s.URL.String(),
		true,
		templateName,
		"",
		"",
		WithPool(pool),
		WithCloudInitMutationCommand(scriptPath),
	)
	require.NoError(t, err)

	// Get the bare client
	bareClient := c.(*client)

	pKey, err := util.GenerateSshKey()
	require.NoError(t, err)
	pubKey, err := util.GetSshPubKey(pKey)
	require.NoError(t, err)

	targetName, err := bareClient.templateClone(t.Context(), hclog.NewNullLogger(), bareClient.template, &GuestOsOpts{
		Username: "test-user",
		PubKey:   pubKey,
	})
	require.NoError(t, err)
	require.Equal(t, true, targetName != "", "targetName was empty")

	scriptOutputBytes, err := os.ReadFile(scriptOutputPath)
	require.NoError(t, err, "expected to read the script output")

	userDataLines := strings.Split(string(scriptOutputBytes), "\n")
	require.Equal(t, fmt.Sprintf("hostname: '%s'", targetName), userDataLines[len(userDataLines)-2],
		"expected to find added line to cloudinit")
}
