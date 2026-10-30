//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func dnsOnlyPreviousPath() string { return filepath.Join(stateDirectory(), "dns-only-previous.json") }

func dnsOnlyActive() bool {
	if _, err := os.Stat(filepath.Join(stateDirectory(), "dns-only-system.json")); !errors.Is(err, os.ErrNotExist) {
		return true
	}
	config, err := loadDNSConfig()
	return err == nil && config.DNSOnly
}

func restorePreviousDNSService(config dnsConfig) error {
	running, err := tunnelRunning()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(stateDirectory(), "warp.conf")); errors.Is(err, os.ErrNotExist) {
		// DNS-only works before WARP installation. Do not leave a service
		// depending on a tunnel that does not exist.
		disabled := config
		disabled.Enabled = false
		if err := syncDNSService(disabled, false); err != nil {
			return err
		}
		return writeDNSConfig(config)
	}
	return syncDNSService(config, running)
}

func stopDNSOnly(resumeWarp bool) (string, error) {
	previous := defaultDNSConfig()
	data, err := os.ReadFile(dnsOnlyPreviousPath())
	if err == nil {
		if err = json.Unmarshal(data, &previous); err != nil {
			return "", fmt.Errorf("копия прежних настроек DNS: %w", err)
		}
		previous.DNSOnly = false
		if err = validateDNSConfig(previous); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	arguments := []string{"-Action", "StopDNSOnly"}
	if resumeWarp {
		arguments = append(arguments, "-ResumePreviousWarp")
	}
	output, err := runPowerShell(arguments)
	if err != nil {
		// In particular, keep the local resolver alive if restoration failed.
		return output, err
	}
	if err = restorePreviousDNSService(previous); err != nil {
		return output, err
	}
	if err = os.Remove(dnsOnlyPreviousPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return output, err
	}
	return output + "\r\nРежим «Только DNS» выключен.", nil
}

func startDNSOnly(config dnsConfig) (string, error) {
	config.Enabled = true
	config.DNSOnly = true
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	prepared, err := prepareDNSConfig(ctx, config)
	cancel()
	if err != nil {
		return "", err
	}
	previous, err := loadDNSConfig()
	if err != nil {
		return "", err
	}
	active := dnsOnlyActive()
	if !active {
		if _, err := os.Stat(dnsOnlyPreviousPath()); err == nil {
			// A previous stop restored the system DNS but did not finish
			// restoring preferences. Finish it before starting a new mode.
			if _, err = stopDNSOnly(false); err != nil {
				return "", err
			}
			previous, err = loadDNSConfig()
			if err != nil {
				return "", err
			}
		}
		if err = writeProtectedJSON(dnsOnlyPreviousPath(), previous); err != nil {
			return "", err
		}
	}
	output := ""
	if !active {
		output, err = runPowerShell([]string{"-Action", "PrepareDNSOnly"})
	}
	if err == nil {
		err = syncDNSService(prepared, true)
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = verifyDNSServer(ctx, prepared)
		cancel()
	}
	if err == nil {
		var applied string
		applied, err = runPowerShell([]string{"-Action", "ApplyDNSOnly"})
		output += applied
	}
	if err != nil {
		var rollback error
		if active {
			rollback = syncDNSService(previous, true)
		} else {
			var restored string
			restored, rollback = stopDNSOnly(true)
			output += restored
		}
		if rollback != nil {
			return output, fmt.Errorf("только DNS: %w; восстановление настроек: %v", err, rollback)
		}
		return output + "\r\nПрежние настройки восстановлены.", err
	}
	return output + "\r\nDNS: " + prepared.Type + " " + prepared.Address, nil
}
