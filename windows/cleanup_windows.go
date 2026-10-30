//go:build windows

package main

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	utf16codec "unicode/utf16"
)

// Windows holds the running image open. A detached, fileless worker waits for
// this process to exit and removes only that exact executable, never its folder.
func scheduleLauncherRemoval() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	literal := "'" + strings.ReplaceAll(executable, "'", "''") + "'"
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'; Wait-Process -Id %d -ErrorAction SilentlyContinue; for($i=0;$i -lt 40;$i++){try{Remove-Item -LiteralPath %s -Force;break}catch{Start-Sleep -Milliseconds 250}}`, os.Getpid(), literal)
	words := utf16codec.Encode([]rune(script))
	data := make([]byte, len(words)*2)
	for index, word := range words {
		binary.LittleEndian.PutUint16(data[index*2:], word)
	}
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	command := exec.Command(filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(data))
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
