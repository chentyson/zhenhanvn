package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed app.zip
var appZip embed.FS

func main() {
	// Get the directory where this exe lives
	exePath, err := os.Executable()
	if err != nil {
		fatal("cannot determine exe path: %v", err)
	}
	exeDir := filepath.Dir(exePath)

	// Target directory: next to the exe
	targetDir := filepath.Join(exeDir, "zhenhanvn")

	// Check if already extracted
	if !isComplete(targetDir) {
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			fatal("cannot create target dir: %v", err)
		}
		if err := extractZip(targetDir); err != nil {
			fatal("extraction failed: %v", err)
		}
	}

	// Launch the main application
	appExe := filepath.Join(targetDir, "zhenhanvn.exe")
	cmd := exec.Command(appExe)
	cmd.Dir = targetDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fatal("cannot launch app: %v", err)
	}
	// Don't wait - let the app run independently
}

func isComplete(dir string) bool {
	for _, f := range []string{"zhenhanvn.exe", "zhenhanvn.dll", "coreclr.dll"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return false
		}
	}
	return true
}

func extractZip(targetDir string) error {
	data, err := appZip.ReadFile("app.zip")
	if err != nil {
		return fmt.Errorf("cannot read embedded zip: %w", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("invalid zip: %w", err)
	}

	for _, file := range reader.File {
		path := filepath.Join(targetDir, file.Name)

		// Security: prevent zip slip
		if !filepath.IsAbs(path) && !strings.HasPrefix(filepath.Clean(path), filepath.Clean(targetDir)) {
			continue
		}

		if file.FileInfo().IsDir() {
			os.MkdirAll(path, 0755)
			continue
		}

		// Create parent directory
		os.MkdirAll(filepath.Dir(path), 0755)

		rc, err := file.Open()
		if err != nil {
			return err
		}

		outFile, err := os.Create(path)
		if err != nil {
			rc.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		rc.Close()
		outFile.Close()
		if err != nil {
			return err
		}

		// Preserve permissions (executable bit)
		mode := file.Mode()
		if mode&0111 != 0 {
			os.Chmod(path, 0755)
		}
	}

	return nil
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "zhenhanvn launcher: "+format+"\n", args...)
	os.Exit(1)
}
