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

const (
	FILE_FLAG_NO_BUFFERING    = 0x20000000
	FILE_FLAG_SEQUENTIAL_SCAN = 0x08000000
	GENERIC_READ              = 0x80000000
	GENERIC_WRITE             = 0x40000000
	FILE_SHARE_READ           = 0x00000001
	FILE_SHARE_WRITE          = 0x00000002
	OPEN_EXISTING             = 3
	INVALID_HANDLE_VALUE      = ^uintptr(0)
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
	procGetDiskFreeSpaceW   = kernel32.NewProc("GetDiskFreeSpaceW")
	procCreateFileW         = kernel32.NewProc("CreateFileW")
	procCloseHandle         = kernel32.NewProc("CloseHandle")
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

func openFileDirectRead(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("路径转换失败: %v", err)
	}
	handle, _, err := procCreateFileW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(GENERIC_READ),
		uintptr(FILE_SHARE_READ|FILE_SHARE_WRITE),
		0,
		uintptr(OPEN_EXISTING),
		uintptr(FILE_FLAG_NO_BUFFERING|FILE_FLAG_SEQUENTIAL_SCAN),
		0,
	)
	if handle == INVALID_HANDLE_VALUE {
		return nil, fmt.Errorf("CreateFileW 失败: %v", err)
	}
	return os.NewFile(handle, path), nil
}

func getVolumeSectorSize(path string) int {
	root := path
	if len(root) < 2 || root[1] != ':' {
		return 512
	}
	if !strings.HasSuffix(root, "\\") {
		root = root[:2] + "\\"
	}

	var bytesPerSector uint32
	var sectorsPerCluster uint32
	var freeClusters uint32
	var totalClusters uint32

	ret, _, _ := procGetDiskFreeSpaceW.Call(
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(root))),
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

	sectorSize := getWindowsSectorSize(rawPath)
	deviceSize, err := getWindowsDeviceSize(mountPath)
	if err != nil {
		return nil, fmt.Errorf("无法获取设备大小: %v", err)
	}

	testBytes := deviceSize
	totalSectors := int(testBytes / uint64(sectorSize))

	fmt.Printf("  设备总大小: %s\n", formatBytes(deviceSize))
	fmt.Printf("  测试范围:   %s (%d 个扇区)\n", formatBytes(testBytes), totalSectors)
	fmt.Printf("  扇区大小:   %d bytes\n", sectorSize)

	var badReadSectors int32
	var badWriteSectors int32

	fmt.Println("\n  [阶段1/2] 只读扫描...")
	f, err := os.OpenFile(rawPath, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("无法打开原始设备 (需要管理员权限): %v", err)
	}

	for sec := 0; sec < totalSectors; sec++ {
		offset := int64(sec) * int64(sectorSize)
		readBuf := make([]byte, sectorSize)
		_, err := f.ReadAt(readBuf, offset)
		if err != nil {
			atomic.AddInt32(&badReadSectors, 1)
			fmt.Printf("  ⚠ 扇区 %d 读取错误: %v\n", sec, err)
		}
		if (sec+1)%100 == 0 {
			fmt.Printf("  读取进度: %d/%d 扇区 (%.0f%%), 已发现 %d 个坏扇区\n",
				sec+1, totalSectors, float64(sec+1)/float64(totalSectors)*100, atomic.LoadInt32(&badReadSectors))
		}
	}
	f.Close()

	fmt.Println("\n  [阶段2/2] 写入测试...")
	fmt.Println("  警告: 将对扇区进行写入-验证-恢复操作，请勿中断!")
	wf, err := openFileDirectWrite(rawPath)
	if err != nil {
		return nil, fmt.Errorf("无法以写入模式打开原始设备: %v", err)
	}
	defer wf.Close()

	writeSectors := totalSectors
	writeStep := 1

	pattern := generateSectorPattern(0, sectorSize)
	originalData := make([]byte, sectorSize)
	verifyBuf := make([]byte, sectorSize)

	for i := 0; i < writeSectors; i++ {
		sec := i * writeStep
		offset := int64(sec) * int64(sectorSize)

		_, err := wf.ReadAt(originalData, offset)
		if err != nil {
			atomic.AddInt32(&badWriteSectors, 1)
			fmt.Printf("  ⚠ 扇区 %d 写入前读取失败: %v\n", sec, err)
			continue
		}

		_, err = wf.WriteAt(pattern, offset)
		if err != nil {
			atomic.AddInt32(&badWriteSectors, 1)
			fmt.Printf("  ⚠ 扇区 %d 写入失败: %v\n", sec, err)
			continue
		}

		_, err = wf.ReadAt(verifyBuf, offset)
		if err != nil {
			atomic.AddInt32(&badWriteSectors, 1)
			fmt.Printf("  ⚠ 扇区 %d 写入后读取失败: %v\n", sec, err)
			wf.WriteAt(originalData, offset)
			continue
		}

		verifyOK := true
		for j := 0; j < sectorSize; j++ {
			if verifyBuf[j] != pattern[j] {
				verifyOK = false
				break
			}
		}
		if !verifyOK {
			atomic.AddInt32(&badWriteSectors, 1)
			fmt.Printf("  ⚠ 扇区 %d 数据验证失败 (写入数据与读回数据不一致)\n", sec)
		}

		wf.WriteAt(originalData, offset)

		if (i+1)%1000 == 0 {
			fmt.Printf("  写入进度: %d/%d 扇区 (%.0f%%), 已发现 %d 个坏扇区\n",
				i+1, writeSectors, float64(i+1)/float64(writeSectors)*100, atomic.LoadInt32(&badWriteSectors))
		}
	}

	return &RawTestResult{
		Device:          mountPath,
		RawPath:         rawPath,
		SectorsTested:   totalSectors,
		BadReadSectors:  int(atomic.LoadInt32(&badReadSectors)),
		BadWriteSectors: int(atomic.LoadInt32(&badWriteSectors)),
		SectorSize:      sectorSize,
		PatternDesc:     "读取扫描 + 写入-验证-恢复测试",
	}, nil
}

func openFileDirectWrite(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("路径转换失败: %v", err)
	}
	handle, _, err := procCreateFileW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(GENERIC_READ|GENERIC_WRITE),
		uintptr(FILE_SHARE_READ|FILE_SHARE_WRITE),
		0,
		uintptr(OPEN_EXISTING),
		uintptr(FILE_FLAG_NO_BUFFERING|FILE_FLAG_SEQUENTIAL_SCAN),
		0,
	)
	if handle == INVALID_HANDLE_VALUE {
		return nil, fmt.Errorf("CreateFileW 失败: %v", err)
	}
	return os.NewFile(handle, path), nil
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

func formatDrive(path string) error {
	if len(path) < 2 || path[1] != ':' {
		return fmt.Errorf("无效的Windows路径: %s", path)
	}
	drive := path[:2]

	fmt.Printf("正在格式化 %s (exFAT 快速格式化)...\n", drive)
	cmd := exec.Command("format", drive, "/FS:exFAT", "/Q", "/Y")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}