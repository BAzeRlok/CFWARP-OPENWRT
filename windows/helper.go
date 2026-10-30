package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

const maxHelperBytes = 64 << 20

// Read through gzip EOF so truncation and checksum errors prevent execution.
// The limit also bounds extraction if a build accidentally embeds a bad asset.
func unpackHelper(payload []byte, destination io.Writer, limit int64) (string, error) {
	input := bytes.NewReader(payload)
	archive, err := gzip.NewReader(input)
	if err != nil {
		return "", err
	}
	defer archive.Close()
	archive.Multistream(false)
	hash := sha256.New()
	count, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(archive, limit+1))
	if err != nil {
		return "", err
	}
	if count == 0 || count > limit {
		return "", errors.New("недопустимый размер встроенного компонента WARP")
	}
	if input.Len() != 0 {
		return "", errors.New("лишние данные в архиве встроенного компонента WARP")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
