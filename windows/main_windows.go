//go:build windows

package main

import (
	"bufio"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const version = "4.1.1"

//go:embed warp.ps1
var warpScript []byte

var (
	shell32                = syscall.NewLazyDLL("shell32.dll")
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procIsUserAnAdmin      = shell32.NewProc("IsUserAnAdmin")
	procShellExecuteW      = shell32.NewProc("ShellExecuteW")
	procSetConsoleCP       = kernel32.NewProc("SetConsoleCP")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
)

var actions = map[string]string{
	"install":    "Install",
	"connect":    "Connect",
	"disconnect": "Disconnect",
	"reconnect":  "Reconnect",
	"rescan":     "Rescan",
	"status":     "Status",
	"uninstall":  "Uninstall",
}

func setUTF8Console() {
	_, _, _ = procSetConsoleCP.Call(65001)
	_, _, _ = procSetConsoleOutputCP.Call(65001)
}

func isAdministrator() bool {
	result, _, _ := procIsUserAnAdmin.Call()
	return result != 0
}

func elevate() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(executable)
	directory, _ := syscall.UTF16PtrFromString(filepath.Dir(executable))
	quoted := make([]string, 0, len(os.Args)-1)
	for _, argument := range os.Args[1:] {
		quoted = append(quoted, syscall.EscapeArg(argument))
	}
	parameters, _ := syscall.UTF16PtrFromString(strings.Join(quoted, " "))
	result, _, callErr := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(parameters)),
		uintptr(unsafe.Pointer(directory)),
		1,
	)
	if result <= 32 {
		return fmt.Errorf("не удалось запросить права администратора (код %d): %v", result, callErr)
	}
	return nil
}

func canonicalAction(arguments []string) string {
	if len(arguments) == 0 {
		return "Install"
	}
	if action, ok := actions[strings.ToLower(arguments[0])]; ok {
		return action
	}
	for index := 0; index+1 < len(arguments); index++ {
		if strings.EqualFold(arguments[index], "-Action") || strings.EqualFold(arguments[index], "--action") {
			if action, ok := actions[strings.ToLower(arguments[index+1])]; ok {
				return action
			}
		}
	}
	return "Install"
}

func normalizeArguments(arguments []string) ([]string, string, bool, error) {
	if len(arguments) == 1 {
		switch strings.ToLower(arguments[0]) {
		case "--help", "-h", "/?":
			return nil, "", false, errors.New("help")
		case "--version", "-v":
			return nil, "", false, errors.New("version")
		}
	}

	action := canonicalAction(arguments)
	normalized := make([]string, 0, len(arguments)+2)
	if len(arguments) == 0 {
		normalized = append(normalized, "-Action", action)
	} else if canonical, ok := actions[strings.ToLower(arguments[0])]; ok {
		normalized = append(normalized, "-Action", canonical)
		arguments = arguments[1:]
	}

	accepted := false
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch strings.ToLower(argument) {
		case "--accept-cloudflare-terms":
			normalized = append(normalized, "-AcceptCloudflareTerms")
			accepted = true
		case "--action":
			normalized = append(normalized, "-Action")
		case "--sample":
			normalized = append(normalized, "-Sample")
		case "--mtu":
			normalized = append(normalized, "-Mtu")
		case "--sni":
			normalized = append(normalized, "-Sni")
		case "--required-countries":
			normalized = append(normalized, "-RequiredCountries")
		case "--required-nodes":
			normalized = append(normalized, "-RequiredNodes")
		case "--excluded-countries":
			normalized = append(normalized, "-ExcludedCountries")
		case "--excluded-nodes":
			normalized = append(normalized, "-ExcludedNodes")
		case "--dns-servers":
			normalized = append(normalized, "-DnsServers")
		default:
			normalized = append(normalized, argument)
			if strings.EqualFold(argument, "-AcceptCloudflareTerms") {
				accepted = true
			}
		}
	}
	return normalized, action, accepted, nil
}

func acceptTerms() bool {
	fmt.Println("Для регистрации WARP необходимо принять условия Cloudflare:")
	fmt.Println("https://www.cloudflare.com/application/terms/")
	fmt.Println("https://www.cloudflare.com/privacypolicy/")
	fmt.Print("Продолжить регистрацию? [y/N]: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "д", "да":
		return true
	default:
		return false
	}
}

func printUsage() {
	fmt.Printf(`WARP Windows client v%s

Использование:
  warp-windows.exe
  warp-windows.exe Status
  warp-windows.exe Connect|Disconnect|Reconnect
  warp-windows.exe Rescan --sample 8
  warp-windows.exe Uninstall

Для автоматической установки без вопроса:
  warp-windows.exe Install --accept-cloudflare-terms
`, version)
}

func waitForEnter() {
	fmt.Print("Нажмите Enter для выхода...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func runPowerShell(arguments []string) error {
	temporaryDirectory, err := os.MkdirTemp("", "warp-windows-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporaryDirectory)
	scriptPath := filepath.Join(temporaryDirectory, "warp.ps1")
	if err := os.WriteFile(scriptPath, warpScript, 0600); err != nil {
		return err
	}
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	powerShell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	commandArguments := []string{
		"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath,
	}
	commandArguments = append(commandArguments, arguments...)
	command := exec.Command(powerShell, commandArguments...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func main() {
	setUTF8Console()
	originalArguments := append([]string(nil), os.Args[1:]...)
	arguments, action, accepted, err := normalizeArguments(originalArguments)
	if err != nil {
		switch err.Error() {
		case "help":
			printUsage()
			return
		case "version":
			fmt.Println(version)
			return
		default:
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}

	if !strings.EqualFold(action, "Status") && !isAdministrator() {
		if err := elevate(); err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка:", err)
			os.Exit(1)
		}
		return
	}

	interactive := len(originalArguments) == 0
	if (action == "Install" || action == "Rescan") && !accepted {
		if !acceptTerms() {
			fmt.Println("Регистрация отменена.")
			if interactive {
				waitForEnter()
			}
			return
		}
		arguments = append(arguments, "-AcceptCloudflareTerms")
	}

	if err := runPowerShell(arguments); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			fmt.Fprintf(os.Stderr, "WARP завершился с кодом %d.\n", exitError.ExitCode())
		} else {
			fmt.Fprintln(os.Stderr, "Не удалось запустить WARP:", err)
		}
		if interactive {
			waitForEnter()
		}
		os.Exit(1)
	}
	if interactive {
		waitForEnter()
	}
}
