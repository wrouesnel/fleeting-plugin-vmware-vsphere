package util

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
)

func ReadEncodedGzippedString(encoded string) (string, error) {
	compressed, err := base64.StdEncoding.DecodeString(encoded)
	gzrdr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", err
	}

	decoded, err := io.ReadAll(gzrdr)
	return string(decoded), err
}
