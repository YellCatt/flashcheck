//go:build darwin
// +build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
)

func getExecutableDrivePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("获取可执行文件路径失败: %v", err)
	}

	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("解析可执行文件路径失败: %v", err)
	}

	exeDir := filepath.Dir(exe)

	var prevDev uint64
	for {
		var stat syscall.Stat_t
		if err := syscall.Stat(exeDir, &stat); err != nil {
			break
		}
		dev := uint64(stat.Dev)
		if prevDev != 0 && dev != prevDev {
			break
		}
		prevDev = dev

		parent := filepath.Dir(exeDir)
		if parent == exeDir {
			break
		}
		exeDir = parent
	}
	return exeDir, nil
}

func openFileDirectRead(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_NOCACHE, 1)
	if errno != 0 {
		f.Close()
		return nil, errno
	}
	return f, nil
}

func getVolumeSectorSize(path string) int {
	return 512
}

func listRemovableDevices() ([]DeviceInfo, error) {
	cmd := exec.Command("diskutil", "list", "-external", "-plist")
	out, err := cmd.Output()
	if err != nil {
		return listMountedVolumesMac()
	}

	var devices []DeviceInfo
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "Mount Point") || strings.Contains(line, "Volume Name") {
			mount := extractPlistValue(line, "Mount Point")
			name := extractPlistValue(line, "Volume Name")
			sizeStr := extractPlistValue(line, "Size")

			if mount == "" {
				continue
			}

			size, _ := strconv.ParseUint(sizeStr, 10, 64)
			if size == 0 {
				total, _, err := getDiskUsage(mount)
				if err == nil {
					size = total
				}
			}

			if name == "" {
				name = filepath.Base(mount)
			}

			devices = append(devices, DeviceInfo{
				Index:     len(devices),
				Name:      name,
				Path:      mount,
				Removable: true,
				Size:      size,
			})
		}
	}

	if len(devices) == 0 {
		return listMountedVolumesMac()
	}
	return devices, nil
}

func listMountedVolumesMac() ([]DeviceInfo, error) {
	cmd := exec.Command("df", "-h")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("df 执行失败: %v", err)
	}

	var devices []DeviceInfo
	lines := strings.Split(string(out), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		path := fields[8]
		if !strings.Contains(path, "Volumes") {
			continue
		}
		if path == "/Volumes" {
			continue
		}

		total, _, err := getDiskUsage(path)
		if err != nil {
			continue
		}

		devices = append(devices, DeviceInfo{
			Index:     len(devices),
			Name:      filepath.Base(path),
			Path:      path,
			Removable: true,
			Size:      total,
		})
	}
	return devices, nil
}

func getDiskUsage(path string) (total, free uint64, err error) {
	var stat syscall.Statfs_t
	err = syscall.Statfs(path, &stat)
	if err != nil {
		return 0, 0, fmt.Errorf("Statfs 失败: %v", err)
	}

	bsize := uint64(stat.Bsize)
	if bsize == 0 {
		bsize = 4096
	}

	total = bsize * stat.Blocks
	free = bsize * stat.Bavail
	return total, free, nil
}

func resolveRawDevice(mountPath string) (string, error) {
	cmd := exec.Command("df", mountPath)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("df 命令失败: %v", err)
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			device := fields[0]
			if strings.HasPrefix(device, "/dev/") {
				if strings.HasPrefix(device, "/dev/disk") {
					rawDevice := strings.Replace(device, "/dev/disk", "/dev/rdisk", 1)
					return rawDevice, nil
				}
				return device, nil
			}
		}
	}

	return "", fmt.Errorf("未找到挂载点 %s 对应的设备", mountPath)
}

func rawSectorTest(mountPath string) (*RawTestResult, error) {
	rawPath, err := resolveRawDevice(mountPath)
	if err != nil {
		return nil, err
	}

	fmt.Printf("  原始设备: %s\n", rawPath)

	f, err := os.OpenFile(rawPath, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("无法打开原始设备 (需要root权限): %v", err)
	}
	defer f.Close()

	fd := int(f.Fd())

	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("无法获取设备信息: %v", err)
	}
	deviceSize := uint64(stat.Size)

	sectorSize := getMacSectorSize(rawPath)

	testBytes := deviceSize
	if testBytes > 1024*1024*1024 {
		testBytes = 1024 * 1024 * 1024
	}
	totalSectors := int(testBytes / uint64(sectorSize))

	fmt.Printf("  设备总大小: %s\n", formatBytes(deviceSize))
	fmt.Printf("  测试范围:   %s (%d 个扇区)\n", formatBytes(testBytes), totalSectors)
	fmt.Printf("  扇区大小:   %d bytes\n", sectorSize)

	var badSectors int32

	for sec := 0; sec < totalSectors; sec++ {
		offset := int64(sec) * int64(sectorSize)

		readBuf := make([]byte, sectorSize)
		_, err := f.ReadAt(readBuf, offset)
		if err != nil {
			atomic.AddInt32(&badSectors, 1)
			fmt.Printf("  ⚠ 扇区 %d 读取错误: %v\n", sec, err)
			continue
		}

		if (sec+1)%100 == 0 {
			fmt.Printf("  进度: %d/%d 扇区 (%.0f%%), 已发现 %d 个坏扇区\n",
				sec+1, totalSectors, float64(sec+1)/float64(totalSectors)*100, atomic.LoadInt32(&badSectors))
		}
	}

	return &RawTestResult{
		Device:        mountPath,
		RawPath:       rawPath,
		SectorsTested: totalSectors,
		BadSectors:    int(atomic.LoadInt32(&badSectors)),
		SectorSize:    sectorSize,
		PatternDesc:   "只读扫描（无写入）",
	}, nil
}

func getMacSectorSize(devicePath string) int {
	cmd := exec.Command("diskutil", "info", devicePath)
	out, err := cmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "Block Size") {
				fields := strings.SplitN(line, ":", 2)
				if len(fields) == 2 {
					sizeStr := strings.TrimSpace(fields[1])
					size, err := strconv.Atoi(sizeStr)
					if err == nil && size > 0 {
						return size
					}
				}
			}
		}
	}

	f, err := os.OpenFile(devicePath, os.O_RDONLY, 0)
	if err == nil {
		defer f.Close()
		fd := int(f.Fd())
		var stat syscall.Stat_t
		if syscall.Fstat(fd, &stat) == nil {
			if stat.Blksize > 0 {
				return int(stat.Blksize)
			}
		}
	}

	return 512
}

func extractPlistValue(line, key string) string {
	keyLower := strings.ToLower(key)
	lineLower := strings.ToLower(line)
	if !strings.Contains(lineLower, keyLower) {
		return ""
	}
	idx := strings.Index(lineLower, keyLower)
	rest := line[idx+len(key):]
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "=") {
		rest = strings.TrimSpace(rest[1:])
	}
	if strings.HasPrefix(rest, "\"") {
		rest = rest[1:]
		quoteIdx := strings.Index(rest, "\"")
		if quoteIdx >= 0 {
			return rest[:quoteIdx]
		}
	}
	sepIdx := strings.IndexAny(rest, ",};")
	if sepIdx >= 0 {
		return strings.TrimSpace(rest[:sepIdx])
	}
	return strings.TrimSpace(rest)
}
