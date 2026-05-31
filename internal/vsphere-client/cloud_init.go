package vsphereclient

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/vmware/govmomi/vim25/types"
	"gopkg.in/yaml.v3"
)

type user struct {
	Name         string   `yaml:"name,omitempty"`
	PrimaryGroup string   `yaml:"primary_group,omitempty"`
	Sudo         string   `yaml:"sudo,omitempty"`
	Groups       string   `yaml:"groups,omitempty"`
	LockPasswd   bool     `yaml:"lock_passwd,omitempty"`
	SshKeys      []string `yaml:"ssh_authorized_keys,omitempty"`
}

type cloudInitConfig struct {
	Users []user `yaml:"users"`
}

func (c *client) encodeUserData(ctx context.Context, username string, pubKey []byte, targetName string) ([]types.BaseOptionValue, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)

	if _, err := gw.Write(pubKey); err != nil {
		return nil, fmt.Errorf("compressing cloud-init user data: %w", err)
	}

	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("compressing cloud-init user data: %w", err)
	}

	data := cloudInitConfig{
		Users: []user{
			{
				Name:         username,
				PrimaryGroup: username,
				Sudo:         "ALL=(ALL) NOPASSWD:ALL",
				Groups:       "sudo, wheel",
				LockPasswd:   false,
				SshKeys: []string{
					string(pubKey),
				},
			},
		},
	}

	marshalled, err := yaml.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("configuring cloud-init user data: %w", err)
	}

	if c.cloudInitCommand != nil {
		// Run the cloud init mutation hook if present.
		cloudInitFile, err := os.CreateTemp("", "fleeting-plugin-vmware-vsphere-cloudinit.*.yaml")
		if err != nil {
			return nil, fmt.Errorf("cloud-init-hook-script - error creating temp file: %w", err)
		}
		defer func() {
			// TODO: would be nice to be able to log problems here...
			cloudInitFile.Close()
			os.Remove(cloudInitFile.Name())
		}()

		_, err = io.Copy(cloudInitFile, bytes.NewReader(marshalled))
		if err != nil {
			return nil, fmt.Errorf("cloud-init-hook-script - error writing content: %w", err)
		}
		err = cloudInitFile.Sync()
		if err != nil {
			return nil, fmt.Errorf("cloud-init-hook-script - error syncing content: %w", err)
		}
		// Execute the command. If the command fails then we're going to fail the operation.
		err = c.cloudInitCommand.Run(ctx, map[string]string{
			"CLOUDINIT_PATH": cloudInitFile.Name(),
			"TARGET_NAME":    targetName,
		})
		if err != nil {
			return nil, fmt.Errorf("cloud-init-hook-script - error executing: %w", err)
		}

		// Reload the mutated cloud-init.
		_, err = cloudInitFile.Seek(0, io.SeekStart)
		if err != nil {
			return nil, fmt.Errorf("cloud-init-hook-script - error rewinding: %w", err)
		}
		marshalled, err = io.ReadAll(cloudInitFile)
		if err != nil {
			return nil, fmt.Errorf("cloud-init-hook-script - error reading content: %w", err)
		}
		// Check that we got something sensible back
		if len(marshalled) == 0 {
			return nil, errors.New("cloud-init does not contain any data after hook script handling")
		}
	}

	config := fmt.Sprintf("#cloud-config\n\n%s", marshalled)

	encoded := base64.StdEncoding.EncodeToString([]byte(config))

	options := []types.BaseOptionValue{
		&types.OptionValue{
			Key:   "guestinfo.userdata",
			Value: encoded,
		},
		&types.OptionValue{
			Key:   "guestinfo.userdata.encoding",
			Value: "base64",
		},
	}

	return options, nil
}
