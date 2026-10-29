//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	win "golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const dnsServiceName = "WarpExternalDNS"
const tunnelServiceName = "AmneziaWGTunnel$warp"

func stateDirectory() string {
	path := os.Getenv("ProgramData")
	if path == "" {
		path = `C:\ProgramData`
	}
	return filepath.Join(path, "WARP")
}

func dnsConfigPath() string  { return filepath.Join(stateDirectory(), "dns.json") }
func dnsServicePath() string { return filepath.Join(stateDirectory(), "warp-dns-service.exe") }
func dnsLogPath() string     { return filepath.Join(stateDirectory(), "dns.log") }

func loadDNSConfig() (dnsConfig, error) {
	config := defaultDNSConfig()
	data, err := os.ReadFile(dnsConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	if err = json.Unmarshal(data, &config); err != nil {
		return config, err
	}
	return config, validateDNSConfig(config)
}

func protectPath(path string, directory bool) error {
	permission := "F"
	if directory {
		permission = "(OI)(CI)F"
	}
	command := exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", "*S-1-5-18:"+permission, "*S-1-5-32-544:"+permission)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ACL %s: %w: %s", path, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func replaceFile(source, destination string) error {
	from, err := win.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := win.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return retryStoppedExecutable(func() error {
		return win.MoveFileEx(from, to, win.MOVEFILE_REPLACE_EXISTING|win.MOVEFILE_WRITE_THROUGH)
	})
}

// SCM can report Stopped just before the process releases its executable.
func retryStoppedExecutable(operation func() error) error {
	deadline := time.Now().Add(4 * time.Second)
	for {
		err := operation()
		if err == nil || time.Now().After(deadline) ||
			(!errors.Is(err, win.ERROR_SHARING_VIOLATION) && !errors.Is(err, win.ERROR_ACCESS_DENIED)) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func writeDNSConfig(config dnsConfig) error {
	if err := os.MkdirAll(stateDirectory(), 0700); err != nil {
		return err
	}
	if err := protectPath(stateDirectory(), true); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	temporary := dnsConfigPath() + ".new"
	defer os.Remove(temporary)
	if err = os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	if err = protectPath(temporary, false); err != nil {
		return err
	}
	return replaceFile(temporary, dnsConfigPath())
}

func copyCurrentExecutable() error {
	source, err := os.Executable()
	if err != nil {
		return err
	}
	destination := dnsServicePath()
	if strings.EqualFold(source, destination) {
		return nil
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary := destination + ".new"
	defer os.Remove(temporary)
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err = protectPath(temporary, false); err != nil {
		return err
	}
	return replaceFile(temporary, destination)
}

func waitServiceState(service *mgr.Service, wanted svc.State) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		status, err := service.Query()
		if err != nil {
			return err
		}
		if status.State == wanted {
			return nil
		}
		if wanted == svc.Running && status.State == svc.Stopped {
			logData, _ := os.ReadFile(dnsLogPath())
			return fmt.Errorf("служба DNS остановилась (код %d): %s", status.Win32ExitCode, strings.TrimSpace(string(logData)))
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("истекло время ожидания службы DNS")
}

func stopService(service *mgr.Service) error {
	status, err := service.Query()
	if err != nil {
		return err
	}
	if status.State == svc.Stopped {
		return nil
	}
	if status.State != svc.StopPending {
		if _, err = service.Control(svc.Stop); err != nil && !errors.Is(err, win.ERROR_SERVICE_NOT_ACTIVE) {
			return err
		}
	}
	return waitServiceState(service, svc.Stopped)
}

func tunnelRunning() (bool, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return false, err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(tunnelServiceName)
	if errors.Is(err, win.ERROR_SERVICE_DOES_NOT_EXIST) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer service.Close()
	status, err := service.Query()
	return status.State == svc.Running, err
}

func resumeDNSService() error {
	running, err := tunnelRunning()
	if err != nil || !running {
		return err
	}
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(dnsServiceName)
	if err != nil {
		return err
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil || status.State == svc.Running {
		return err
	}
	if err = service.Start(); err != nil {
		return err
	}
	return waitServiceState(service, svc.Running)
}

// Reuse the same EXE as the service. No additional download is needed for DNS.
func syncDNSService(config dnsConfig, running bool) error {
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(dnsServiceName)
	if err != nil && !errors.Is(err, win.ERROR_SERVICE_DOES_NOT_EXIST) {
		return err
	}
	if service != nil {
		defer service.Close()
		if err = stopService(service); err != nil {
			return err
		}
	}
	proxy := config.Enabled && config.Type != "udp"
	if !proxy {
		if service != nil {
			if err = service.Delete(); err != nil {
				return err
			}
		}
		if err = writeDNSConfig(config); err != nil {
			return err
		}
		if err = retryStoppedExecutable(func() error { return os.Remove(dnsServicePath()) }); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err = writeDNSConfig(config); err != nil {
		return err
	}
	if err = copyCurrentExecutable(); err != nil {
		return err
	}
	if service == nil {
		service, err = manager.CreateService(dnsServiceName, dnsServicePath(), mgr.Config{
			DisplayName: "WARP DNS", Description: "Encrypted DNS for WARP",
			StartType: win.SERVICE_AUTO_START, ErrorControl: win.SERVICE_ERROR_NORMAL,
			Dependencies: []string{tunnelServiceName},
		})
		if err != nil {
			return err
		}
		defer service.Close()
	}
	if err = service.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 5 * time.Second}}, 86400); err != nil {
		return err
	}
	if err = service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return err
	}
	if !running {
		return nil
	}
	_ = os.Remove(dnsLogPath())
	if err = service.Start(); err != nil {
		return err
	}
	return waitServiceState(service, svc.Running)
}

func appendDNSLog(err error) {
	if err == nil {
		return
	}
	file, openErr := os.OpenFile(dnsLogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if openErr != nil {
		return
	}
	defer file.Close()
	fmt.Fprintf(file, "%s %v\r\n", time.Now().UTC().Format(time.RFC3339), err)
}

type dnsServiceHandler struct{}

func (*dnsServiceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	statuses <- svc.Status{State: svc.StartPending, WaitHint: 10000}
	config, err := loadDNSConfig()
	if err == nil && (!config.Enabled || config.Type == "udp") {
		err = errors.New("DNS-служба не включена")
	}
	if err != nil {
		appendDNSLog(err)
		return true, 1
	}
	proxy, err := startDNSProxy(config)
	if err != nil {
		appendDNSLog(err)
		return true, 2
	}
	defer proxy.Close()
	status := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	statuses <- status
	for request := range requests {
		switch request.Cmd {
		case svc.Interrogate:
			statuses <- status
		case svc.Stop, svc.Shutdown:
			statuses <- svc.Status{State: svc.StopPending, WaitHint: 10000}
			return false, 0
		}
	}
	return false, 0
}

func runDNSService() error { return svc.Run(dnsServiceName, &dnsServiceHandler{}) }

// Only DNS is rolled back: registration and endpoint selection are left intact.
func restoreDNS(config dnsConfig, servers string) error {
	_, scriptErr := runPowerShell([]string{"-Action", "ApplyDNS", "-DnsServers", servers})
	running, stateErr := tunnelRunning()
	serviceErr := syncDNSService(config, running)
	return errors.Join(scriptErr, stateErr, serviceErr)
}

func configuredDNS(data []byte) string {
	inInterface := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inInterface = strings.EqualFold(line, "[Interface]")
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if inInterface && found && strings.EqualFold(strings.TrimSpace(key), "DNS") {
			return strings.TrimSpace(value)
		}
	}
	return dnsServersForConfig(defaultDNSConfig())
}

func runClientAction(action string, arguments []string, config dnsConfig) (string, error) {
	if action == "Connect" || action == "Reconnect" {
		if _, err := os.Stat(filepath.Join(stateDirectory(), "warp.conf")); errors.Is(err, os.ErrNotExist) {
			return runPowerShell(arguments)
		}
		saved, err := loadDNSConfig()
		if err != nil {
			return "", err
		}
		if saved.Enabled && saved.Type != "udp" {
			// Refresh the installed service from the currently launched EXE.
			if err := syncDNSService(saved, false); err != nil {
				return "", errors.Join(err, resumeDNSService())
			}
		}
		output, err := runPowerShell(arguments)
		if err != nil && saved.Enabled && saved.Type != "udp" {
			err = errors.Join(err, resumeDNSService())
		}
		return output, err
	}
	if action != "Install" && action != "Rescan" && action != "ApplyDNS" {
		return runPowerShell(arguments)
	}
	previous, loadErr := loadDNSConfig()
	if loadErr != nil {
		if action != "ApplyDNS" {
			return "", loadErr
		}
		previous = defaultDNSConfig()
	}
	oldTunnel, readErr := os.ReadFile(filepath.Join(stateDirectory(), "warp.conf"))
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	if action == "ApplyDNS" && errors.Is(readErr, os.ErrNotExist) {
		if err := validateDNSConfig(config); err != nil {
			return "", err
		}
		if err := writeDNSConfig(config); err != nil {
			return "", err
		}
		return "Настройки DNS сохранены. Они будут применены при установке WARP.", nil
	}
	if errors.Is(readErr, os.ErrNotExist) {
		// Before first install there is no prior local DNS service to restore.
		previous = defaultDNSConfig()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	prepared, err := prepareDNSConfig(ctx, config)
	cancel()
	if err != nil {
		return "", err
	}
	arguments = append(arguments, "-DnsServers", dnsServersForConfig(prepared))
	output, err := runPowerShell(arguments)
	if err != nil {
		return output, err
	}
	running, err := tunnelRunning()
	if err == nil {
		err = syncDNSService(prepared, running)
	}
	if err == nil && running && prepared.Enabled && prepared.Type != "udp" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = verifyDNSServer(ctx, prepared)
		cancel()
	}
	if err != nil {
		servers := configuredDNS(oldTunnel)
		if loadErr != nil {
			servers = dnsServersForConfig(previous)
		}
		rollbackErr := restoreDNS(previous, servers)
		if rollbackErr != nil {
			return output, fmt.Errorf("сторонний DNS: %w; восстановление прежнего DNS: %v", err, rollbackErr)
		}
		return output + "\r\nПрежние настройки DNS восстановлены.", err
	}
	if prepared.Enabled {
		output += "\r\nСторонний DNS: " + strings.ToUpper(prepared.Type) + " " + prepared.Address
	} else {
		output += "\r\nСторонний DNS выключен."
	}
	return output, nil
}
