package hostpower

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const keepGroups = "keep-groups"

var renderNode = regexp.MustCompile(`^renderD[0-9]+$`)

func probeGPU() ([]string, string) {
	if runtime.GOOS != "linux" {
		return nil, "GPU passthrough requires native Linux Podman"
	}
	var keepSupplementaryGroups bool
	if os.Geteuid() != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "podman", "info", "--format", "{{.Host.OCIRuntime.Name}}").Output()
		keepSupplementaryGroups = err == nil && strings.TrimSpace(string(out)) == "crun"
	}
	return probeDRM(keepSupplementaryGroups, openGPUDevice)
}

func probeDRM(keepSupplementaryGroups bool, probe func(string, bool) error) ([]string, string) {
	nodes, _ := filepath.Glob("/dev/dri/renderD*")
	return selectGPUDevices(nodes, keepSupplementaryGroups, probe, func(path string) bool {
		vendor, _ := os.ReadFile(filepath.Join("/sys/class/drm", filepath.Base(path), "device/vendor"))
		return strings.TrimSpace(string(vendor)) == "0x1002"
	})
}

func selectGPUDevices(nodes []string, keepSupplementaryGroups bool, probe func(string, bool) error, isAMD func(string) bool) ([]string, string) {
	var devices []string
	hasAMD := false
	reason := "no /dev/dri/renderD* character device; NVIDIA CUDA requires a separate CDI runtime integration"
	for _, path := range nodes {
		if !renderNode.MatchString(filepath.Base(path)) {
			continue
		}
		if err := probe(path, keepSupplementaryGroups); err != nil {
			reason = err.Error()
			continue
		}
		devices = append(devices, path)
		hasAMD = hasAMD || isAMD(path)
	}
	if len(devices) == 0 {
		return nil, reason
	}
	if hasAMD && probe("/dev/kfd", keepSupplementaryGroups) == nil {
		devices = append(devices, "/dev/kfd")
	}
	if keepSupplementaryGroups {
		devices = append(devices, keepGroups)
	}
	return devices, ""
}

func openGPUDevice(path string, keepSupplementaryGroups bool) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("%s is not a character device", path)
	}
	// A host-side open can succeed via supplementary groups which the child
	// loses with other OCI runtimes. Only advertise guaranteed public access
	// in that rootless configuration.
	if os.Geteuid() != 0 && !keepSupplementaryGroups && info.Mode().Perm()&0006 != 0006 {
		return fmt.Errorf("%s requires rootless Podman with crun to preserve device group access", path)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("cannot open %s read/write: %w (check host GPU device permissions)", path, err)
	}
	return f.Close()
}
