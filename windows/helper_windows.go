//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// The helper is needed only to register and scan endpoints. Normal connections
// use the installed AmneziaWG service without extracting or starting it.
func installBundledHelper() (string, error) {
	expected, err := unpackHelper(bundledHelper, io.Discard, maxHelperBytes)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(stateDirectory(), 0700); err != nil {
		return "", err
	}
	if err = protectPath(stateDirectory(), true); err != nil {
		return "", err
	}
	destination := filepath.Join(stateDirectory(), "warp-helper.exe")
	current, err := os.Open(destination)
	if err == nil {
		hash := sha256.New()
		count, readErr := io.Copy(hash, io.LimitReader(current, maxHelperBytes+1))
		closeErr := current.Close()
		if readErr != nil {
			return "", readErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if count <= maxHelperBytes && hex.EncodeToString(hash.Sum(nil)) == expected {
			return expected, protectPath(destination, false)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	output, err := os.CreateTemp(stateDirectory(), "warp-helper-*.new")
	if err != nil {
		return "", err
	}
	temporary := output.Name()
	defer os.Remove(temporary)
	_, copyErr := unpackHelper(bundledHelper, output, maxHelperBytes)
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = protectPath(temporary, false); err != nil {
		return "", err
	}
	if err = replaceFile(temporary, destination); err != nil {
		return "", err
	}
	return expected, nil
}
