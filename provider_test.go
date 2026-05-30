package vsphere

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
	vsphereclient "gitlab.com/santhanuv/fleeting-plugin-vmware-vsphere/internal/vsphere-client"
	"gitlab.com/santhanuv/fleeting-plugin-vmware-vsphere/internal/vsphere-client/fake"
)

func setupFakeClient(t *testing.T, setup func(client *fake.Client)) *InstanceGroup {
	t.Helper()

	oldClient := newClient
	t.Cleanup(func() {
		newClient = oldClient
	})

	newClient = func(ctx context.Context, vsphereUrl string, insecure bool, template string, username string, password string, options ...vsphereclient.ClientOption) (vsphereclient.Client, error) {
		client := fake.New()

		if setup != nil {
			setup(client)
		}

		return client, nil
	}

	return &InstanceGroup{
		Name:       "test-vsphere",
		Datacenter: "test-datacenter",
		Folder:     "test-folder",
		Template:   "test-template",
		CloneType:  "full",
	}
}

func TestInit(t *testing.T) {
	t.Run("context cancels init", func(t *testing.T) {
		group := setupFakeClient(t, nil)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		settings := provider.Settings{
			ConnectorConfig: provider.ConnectorConfig{
				UseStaticCredentials: true,
			},
		}

		_, err := group.Init(ctx, hclog.Default(), settings)

		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("context cancels after init still has working client", func(t *testing.T) {
		group := setupFakeClient(t, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		settings := provider.Settings{
			ConnectorConfig: provider.ConnectorConfig{
				UseStaticCredentials: true,
			},
		}

		_, err := group.Init(ctx, hclog.Default(), settings)
		require.NoError(t, err)

		cancel()

		err = group.Update(ctx, func(instance string, state provider.State) {})
		require.NoError(t, err)
	})

	t.Run("invalid ssh key provided", func(t *testing.T) {
		group := setupFakeClient(t, func(client *fake.Client) {
			client.Instances["pre-existing-1"] = provider.StateRunning
		})
		group.size = 1

		ctx := context.Background()

		settings := provider.Settings{
			ConnectorConfig: provider.ConnectorConfig{
				Protocol: provider.ProtocolSSH,
				Key:      []byte("invalid-key"),
			},
		}

		_, err := group.Init(ctx, hclog.Default(), settings)
		require.ErrorContains(t, err, "reading private key: ssh: no key found")
	})
}

func TestIncrease(t *testing.T) {
	group := setupFakeClient(t, nil)

	ctx := context.Background()

	settings := provider.Settings{
		ConnectorConfig: provider.ConnectorConfig{
			UseStaticCredentials: true,
		},
	}

	var count int
	_, err := group.Init(ctx, hclog.Default(), settings)
	require.NoError(t, err)

	require.NoError(t, group.Update(ctx, func(instance string, state provider.State) {
		count++
	}))
	require.Equal(t, 0, count)

	num, err := group.Increase(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, 2, num)

	count = 0
	require.NoError(t, group.Update(ctx, func(instance string, state provider.State) {
		require.Equal(t, provider.StateRunning, state)
		count++
	}))
	require.Equal(t, 2, count)
	require.Equal(t, 2, int(group.size))
}

func TestDecrease(t *testing.T) {
	group := setupFakeClient(t, func(client *fake.Client) {
		client.Instances["pre-existing-1"] = provider.StateRunning
		client.Instances["pre-existing-2"] = provider.StateRunning
	})
	group.size = 2

	ctx := context.Background()

	settings := provider.Settings{
		ConnectorConfig: provider.ConnectorConfig{
			UseStaticCredentials: true,
		},
	}

	var count int
	_, err := group.Init(ctx, hclog.Default(), settings)
	require.NoError(t, err)

	require.NoError(t, group.Update(ctx, func(instance string, state provider.State) {
		count++
	}))
	require.Equal(t, 2, count)

	deleted, err := group.Decrease(ctx, []string{"pre-existing-1"})
	require.NoError(t, err)
	require.Contains(t, deleted, "pre-existing-1")

	count = 0
	require.NoError(t, group.Update(ctx, func(instance string, state provider.State) {
		require.Equal(t, provider.StateRunning, state)
		count++
	}))
	require.Equal(t, 1, len(deleted))
	require.Equal(t, 1, int(group.size))
}

func TestConnectInfo(t *testing.T) {
	group := setupFakeClient(t, func(client *fake.Client) {
		client.Instances["pre-existing-1"] = provider.StateRunning
	})
	group.size = 1

	ctx := context.Background()
	encodedKey := pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(fake.Key()),
		},
	)

	tests := []struct {
		name   string
		config provider.ConnectorConfig
		assert func(t *testing.T, info provider.ConnectInfo, err error)
	}{
		{
			name: "ssh is default for linux",
			config: provider.ConnectorConfig{
				UseStaticCredentials: false,
				OS:                   "linux",
			},
			assert: func(t *testing.T, info provider.ConnectInfo, err error) {
				require.NoError(t, err)
				require.Equal(t, provider.ProtocolSSH, info.Protocol)
				require.NotEmpty(t, info.Key)
			},
		},
		{
			config: provider.ConnectorConfig{
				Protocol: provider.ProtocolSSH,
				Key:      encodedKey,
			},
			assert: func(t *testing.T, info provider.ConnectInfo, err error) {
				require.NoError(t, err)
				require.Equal(t, info.Protocol, provider.ProtocolSSH)
				require.Equal(t, info.Key, encodedKey)
				require.NotEmpty(t, info.Key)
			},
		},
	}

	for _, tc := range tests {
		t.Run(t.Name(), func(t *testing.T) {
			settings := provider.Settings{
				ConnectorConfig: tc.config,
			}

			var count int
			_, err := group.Init(ctx, hclog.Default(), settings)
			require.NoError(t, err)

			require.NoError(t, group.Update(ctx, func(instance string, state provider.State) {
				count++
			}))
			require.Equal(t, 1, count)

			info, err := group.ConnectInfo(ctx, "pre-existing-1")
			tc.assert(t, info, err)
		})
	}
}
