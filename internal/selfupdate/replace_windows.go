//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func replaceExecutable(path string, data []byte) (bool, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".wln-update-*.exe")
	if err != nil {
		return false, fmt.Errorf("create update beside executable: %w", err)
	}
	newPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(newPath)
		return false, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(newPath)
		return false, err
	}
	helper, err := os.CreateTemp("", "wln-update-*.cmd")
	if err != nil {
		os.Remove(newPath)
		return false, err
	}
	helperPath := helper.Name()
	script := "@echo off\r\n" +
		"set /a tries=0\r\n" +
		":retry\r\n" +
		"move /Y \"" + escapeBatch(newPath) + "\" \"" + escapeBatch(path) + "\" >nul 2>&1\r\n" +
		"if not errorlevel 1 goto done\r\n" +
		"set /a tries+=1\r\n" +
		"if %tries% GEQ 60 goto failed\r\n" +
		"ping 127.0.0.1 -n 2 >nul\r\n" +
		"goto retry\r\n" +
		":done\r\n" +
		"del \"%~f0\"\r\n" +
		"exit /b 0\r\n" +
		":failed\r\n" +
		"del \"" + escapeBatch(newPath) + "\" >nul 2>&1\r\n" +
		"del \"%~f0\"\r\n" +
		"exit /b 1\r\n"
	if _, err := helper.WriteString(script); err != nil {
		helper.Close()
		os.Remove(helperPath)
		os.Remove(newPath)
		return false, err
	}
	if err := helper.Close(); err != nil {
		os.Remove(helperPath)
		os.Remove(newPath)
		return false, err
	}
	cmd := exec.Command("cmd.exe", "/C", helperPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008}
	if err := cmd.Start(); err != nil {
		os.Remove(helperPath)
		os.Remove(newPath)
		return false, fmt.Errorf("start Windows update helper: %w", err)
	}
	return true, nil
}

func escapeBatch(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}
