package fake

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"sync"

	"github.com/google/uuid"
	"github.com/hashicorp/go-hclog"
	vsphereclient "github.com/wrouesnel/fleeting-plugin-vmware-vsphere/internal/vsphere-client"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

type Client struct {
	Instances map[string]provider.State
}

var (
	sshKeyOnce sync.Once
	sshKey     *rsa.PrivateKey
)

func Key() *rsa.PrivateKey {
	sshKeyOnce.Do(func() {
		var err error
		sshKey, err = rsa.GenerateKey(rand.Reader, 4096)
		if err != nil {
			panic(err)
		}
	})

	return sshKey
}

func New() *Client {
	return &Client{
		Instances: map[string]provider.State{},
	}
}

func (c *Client) TemplateClone(ctx context.Context, count uint, log hclog.Logger, guestopts *vsphereclient.GuestOsOpts) (uint, error) {
	for range count {
		name := uuid.NewString()
		c.Instances[name] = provider.StateRunning
	}

	return uint(len(c.Instances)), nil
}

func (c *Client) GetVMs(ctx context.Context, logger hclog.Logger) (map[string]provider.State, error) {
	return c.Instances, nil
}

func (c *Client) NetInfo(ctx context.Context, vmName string) (string, error) {
	return "10.0.0.1", nil
}

func (c *Client) DeleteVMs(ctx context.Context, vmNames []string, log hclog.Logger) ([]string, error) {
	var deleted []string

	for _, name := range vmNames {
		if _, ok := c.Instances[name]; ok {
			delete(c.Instances, name)
			deleted = append(deleted, name)
		}
	}

	return deleted, nil
}

func (c *Client) GuestOs(ctx context.Context) (string, error) {
	return "ubuntu64Guest", nil
}
