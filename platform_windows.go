//go:build windows
// +build windows

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
	"unsafe"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
	procGetDiskFreeSpaceW   = kernel32.NewProc("GetDiskFreeSpaceW")
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

	if len(exe) < 2 || exe[1] != ':' {
		return "", fmt.Errorf("无法识别的Windows路径: %s", exe)
	}

	drive := strings.ToUpper(exe[:2]) + "\\"
	return drive, nil
}

func listRemovableDevices() ([]DeviceInfo, error) {
	cmd := exec.Command("wmic", "logicaldisk", "get", "DeviceID,VolumeName,Size,DriveType", "/format:csv")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("wmic 执行失败: %v", err)
	}

	var devices []DeviceInfo
	lines := strings.Split(string(out), "\n")
	idx := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 5 {
			continue
		}
		deviceID := strings.TrimSpace(fields[1])
		volumeName := strings.TrimSpace(fields[2])
		sizeStr := strings.TrimSpace(fields[3])
		driveTypeStr := strings.TrimSpace(fields[4])

		if deviceID == "" || strings.EqualFold(deviceID, "DeviceID") {
			continue
		}

		driveType, _ := strconv.Atoi(driveTypeStr)
		if driveType != 2 {
			continue
		}

		if volumeName == "" {
			volumeName = "Removable Disk"
		}

		size, _ := strconv.ParseUint(sizeStr, 10, 64)

		devices = append(devices, DeviceInfo{
			Index:     idx,
			Name:      volumeName,
			Path:      deviceID + "\\",
			Removable: true,
			Size:      size,
		})
		idx++
	}
	return devices, nil
}

func getDiskUsage(path string) (total, free uint64, err error) {
	root := path
	if len(root) < 2 || root[1] != ':' {
		return 0, 0, fmt.Errorf("无效的Windows路径: %s", path)
	}
	if !strings.HasSuffix(root, "\\") {
		root = root[:2] + "\\"
	}

	var freeAvailable uint64
	var totalNumberOfBytes uint64
	var totalNumberOfFreeBytes uint64

	ret, _, callErr := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(root))),
		uintptr(unsafe.Pointer(&freeAvailable)),
		uintptr(unsafe.Pointer(&totalNumberOfBytes)),
		uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)
	if ret == 0 {
		return 0, 0, fmt.Errorf("GetDiskFreeSpaceEx 失败: %v", callErr)
	}

	return totalNumberOfBytes, totalNumberOfFreeBytes, nil
}

func resolveRawDevice(mountPath string) (string, error) {
	driveLetter := strings.ToUpper(strings.TrimSuffix(strings.TrimSuffix(mountPath, "\\"), ":"))
	if len(driveLetter) != 1 || driveLetter < "A" || driveLetter > "Z" {
		return "", fmt.Errorf("无法解析盘符: %s", mountPath)
	}
	return "\\\\.\\" + driveLetter + ":", nil
}

func rawSectorTest(mountPath string) (*RawTestResult, error) {
	rawPath, err := resolveRawDevice(mountPath)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(rawPath, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("无法打开原始设备 (需要管理员权限): %v", err)
	}
	defer f.Close()

	sectorSize := getWindowsSectorSize(rawPath)
	deviceSize, err := getWindowsDeviceSize(mountPath)
	if err != nil {
		return nil, fmt.Errorf("无法获取设备大小: %v", err)
	}

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

func getWindowsSectorSize(rawPath string) int {
	var bytesPerSector uint32
	var sectorsPerCluster uint32
	var freeClusters uint32
	var totalClusters uint32

	ret, _, _ := procGetDiskFreeSpaceW.Call(
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(rawPath))),
		uintptr(unsafe.Pointer(&sectorsPerCluster)),
		uintptr(unsafe.Pointer(&bytesPerSector)),
		uintptr(unsafe.Pointer(&freeClusters)),
		uintptr(unsafe.Pointer(&totalClusters)),
	)
	if ret != 0 && bytesPerSector > 0 {
		return int(bytesPerSector)
	}
	return 512
}

func getWindowsDeviceSize(path string) (uint64, error) {
	root := path
	if len(root) < 2 || root[1] != ':' {
		return 0, fmt.Errorf("无效的Windows路径")
	}
	if !strings.HasSuffix(root, "\\") {
		root = root[:2] + "\\"
	}

	var freeAvailable uint64
	var totalNumberOfBytes uint64
	var totalNumberOfFreeBytes uint64

	ret, _, err := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(root))),
		uintptr(unsafe.Pointer(&freeAvailable)),
		uintptr(unsafe.Pointer(&totalNumberOfBytes)),
		uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)
	if ret == 0 {
		return 0, fmt.Errorf("GetDiskFreeSpaceEx 失败: %v", err)
	}
	return totalNumberOfBytes, nil
}
