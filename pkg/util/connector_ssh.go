package vsphere

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

type PrivPub interface {
	crypto.PrivateKey
	Public() crypto.PublicKey
}

func GetSshPubKey(privKey []byte) ([]byte, error) {
	priv, err := ssh.ParseRawPrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("reading private key: %w", err)
	}

	key, ok := priv.(PrivPub)
	if !ok {
		return nil, fmt.Errorf("key doesn't export PublicKey()")
	}

	pubkey, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		return nil, fmt.Errorf("generating ssh public key: %w", err)
	}

	return ssh.MarshalAuthorizedKey(pubkey), nil
}

func GenerateSshKey() ([]byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, fmt.Errorf("generating private key: %w", err)
	}

	eKey := pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		})

	if eKey == nil {
		return nil, fmt.Errorf("encoding private key: %w", err)
	}

	return eKey, nil
}
