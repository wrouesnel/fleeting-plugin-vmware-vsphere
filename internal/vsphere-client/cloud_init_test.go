package vsphereclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/vim25/types"
	"github.com/wrouesnel/fleeting-plugin-vmware-vsphere/pkg/util"
)

type fakeClient struct{}

func TestEncodeUserData(t *testing.T) {
	c := &client{}
	username := "testuser"
	pubKey := []byte("ssh-rsa AAAATESTKEY test@example.com")
	targetName := "target_name"

	options, err := c.encodeUserData(context.Background(), hclog.NewNullLogger(), username, pubKey, targetName)
	require.NoError(t, err, "encodeUserData returned error")

	var userData, encoding string
	for _, opt := range options {
		ov := opt.(*types.OptionValue)
		switch ov.Key {
		case "guestinfo.userdata":
			userData = ov.Value.(string)
		case "guestinfo.userdata.encoding":
			encoding = ov.Value.(string)
		}
	}

	require.Equal(t, "gzip+base64", encoding, "expected encoding 'gzip+base64'")

	decoded, err := util.ReadEncodedGzippedString(userData)
	require.NoError(t, err, "failed to read compressed userdata")

	str := string(decoded)
	require.Contains(t, str, "#cloud-config", "cloud-config header missing")
	require.Contains(t, str, username, "username missing in cloud-init YAML")
	require.Contains(t, str, string(pubKey), "ssh key missing in cloud-init YAML")
	require.Contains(t, str, "users:", "users key missing in cloud-init YAML")
	require.Contains(t, str, "sudo, wheel", "required groups missing in cloud-init YAML")
}

// testCloudInitHookScriptSucceeds provides a basic test of cloud-init hook script functionality.
const testCloudInitHookScriptSucceeds = `#!/bin/bash
SOURCE="${BASH_SOURCE[0]}"
while [ -h "$SOURCE" ]; do # resolve $SOURCE until the file is no longer a symlink
  DIR="$( cd -P "$( dirname "$SOURCE" )" >/dev/null 2>&1 && pwd )"
  SOURCE="$(readlink "$SOURCE")"
  [[ $SOURCE != /* ]] && SOURCE="$DIR/$SOURCE" # if $SOURCE was a relative symlink, we need to resolve it relative to the path where the symlink file was located
done
SCRIPT_DIR="$( cd -P "$( dirname "$SOURCE" )" >/dev/null 2>&1 && pwd )"

pushd "${SCRIPT_DIR}"

echo $(pwd) > cloudinit-mutation-script.out
echo "$TARGET_NAME" >> cloudinit-mutation-script.out
echo "$CLOUDINIT_PATH" >> cloudinit-mutation-script.out
# Modify the script
echo "hostname: '$TARGET_NAME'" >> $CLOUDINIT_PATH
echo "---" >> cloudinit-mutation-script.out
cat "$CLOUDINIT_PATH" >> cloudinit-mutation-script.out
exit 0
`

// testCloudInitHookScriptFails deliberately fails.
const testCloudInitHookScriptFails = `#!/bin/bash
SOURCE="${BASH_SOURCE[0]}"
while [ -h "$SOURCE" ]; do # resolve $SOURCE until the file is no longer a symlink
  DIR="$( cd -P "$( dirname "$SOURCE" )" >/dev/null 2>&1 && pwd )"
  SOURCE="$(readlink "$SOURCE")"
  [[ $SOURCE != /* ]] && SOURCE="$DIR/$SOURCE" # if $SOURCE was a relative symlink, we need to resolve it relative to the path where the symlink file was located
done
SCRIPT_DIR="$( cd -P "$( dirname "$SOURCE" )" >/dev/null 2>&1 && pwd )"

pushd "${SCRIPT_DIR}"

echo $(pwd) > cloudinit-mutation-script.out
echo "$TARGET_NAME" >> cloudinit-mutation-script.out
echo $CLOUDINIT_PATH >> cloudinit-mutation-script.out
echo "---" >> cloudinit-mutation-script.out
cat "$CLOUDINIT_PATH" >> cloudinit-mutation-script.out
exit 1
`

func setupSuccessfulCloudInitHookScript(t *testing.T) (temporaryDirectory, scriptPath, scriptOutputPath string) {
	temporaryDirectory = t.TempDir()
	scriptPath = filepath.Join(temporaryDirectory, "cloudinit-mutation-script")
	scriptOutputPath = filepath.Join(temporaryDirectory, "cloudinit-mutation-script.out")

	err := os.WriteFile(scriptPath, []byte(testCloudInitHookScriptSucceeds), os.FileMode(0755))
	require.NoError(t, err)
	return
}

func TestEncodeUserDataWithSuccessfulHookScript(t *testing.T) {
	temporaryDirectory, scriptPath, scriptOutputPath := setupSuccessfulCloudInitHookScript(t)

	c := &client{
		cloudInitCommand: &HostCommand{
			Exe:              scriptPath,
			Args:             nil,
			WorkingDirectory: temporaryDirectory,
			EnvVars:          nil,
		},
	}
	username := "testuser"
	pubKey := []byte("ssh-rsa AAAATESTKEY test@example.com")
	targetName := "target_name"

	options, err := c.encodeUserData(context.Background(), hclog.NewNullLogger(), username, pubKey, targetName)
	require.NoError(t, err, "encodeUserData returned error")

	var userData, encoding string
	for _, opt := range options {
		ov := opt.(*types.OptionValue)
		switch ov.Key {
		case "guestinfo.userdata":
			userData = ov.Value.(string)
		case "guestinfo.userdata.encoding":
			encoding = ov.Value.(string)
		}
	}

	_, err = os.ReadFile(scriptOutputPath)
	require.NoError(t, err, "expected to read the script output")

	require.Equal(t, "gzip+base64", encoding, "expected encoding 'base64'")

	decoded, err := util.ReadEncodedGzippedString(userData)
	require.NoError(t, err, "failed to decode base64")

	str := string(decoded)

	userDataLines := strings.Split(str, "\n")
	require.Equal(t, fmt.Sprintf("hostname: '%s'", targetName), userDataLines[len(userDataLines)-2],
		"expected to find added line to cloudinit")
}

func TestEncodeUserDataWithFailingHookScript(t *testing.T) {
	temporaryDirectory := t.TempDir()
	scriptPath := filepath.Join(temporaryDirectory, "cloudinit-mutation-script")
	//scriptOutputPath := filepath.Join(temporaryDirectory, "cloudinit-mutation-script.out")

	err := os.WriteFile(scriptPath, []byte(testCloudInitHookScriptFails), os.FileMode(0755))
	require.NoError(t, err)

	c := &client{
		cloudInitCommand: &HostCommand{
			Exe:              scriptPath,
			Args:             nil,
			WorkingDirectory: temporaryDirectory,
			EnvVars:          nil,
		},
	}
	username := "testuser"
	pubKey := []byte("ssh-rsa AAAATESTKEY test@example.com")
	targetName := "target_name"

	_, err = c.encodeUserData(context.Background(), hclog.NewNullLogger(), username, pubKey, targetName)
	require.Error(t, err, "encodeUserData should have returned an error")
}
