//go:build linux
// +build linux

package main

import (
	"bufio"
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
	return os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECT, 0)
}

func getVolumeSectorSize(path string) int {
	return 512
}

func listRemovableDevices() ([]DeviceInfo, error) {
	cmd := exec.Command("lsblk", "-J", "-b", "-o", "NAME,SIZE,MOUNTPOINT,TYPE,RM,MODEL,FSTYPE")
	out, err := cmd.Output()
	if err != nil {
		return listMountedVolumes()
	}

	var devices []DeviceInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "\"block\"") || strings.Contains(line, "\"part\"") {
			name := extractJSONString(line, "NAME")
			sizeStr := extractJSONString(line, "SIZE")
			mount := extractJSONString(line, "MOUNTPOINT")
			rm := extractJSONString(line, "RM")
			model := extractJSONString(line, "MODEL")
			fs := extractJSONString(line, "FSTYPE")

			if rm == "1" || (mount != "" && strings.HasPrefix(mount, "/media/")) {
				if mount == "" {
					continue
				}
				size, _ := strconv.ParseUint(sizeStr, 10, 64)
				devName := model
				if devName == "" {
					devName = name
				}
				devices = append(devices, DeviceInfo{
					Index:      len(devices),
					Name:       devName,
					Path:       mount,
					Removable:  true,
					Size:       size,
					Filesystem: fs,
				})
			}
		}
	}

	if len(devices) == 0 {
		return listMountedVolumes()
	}
	return devices, nil
}

func listMountedVolumes() ([]DeviceInfo, error) {
	paths := []string{"/media", "/mnt", "/run/media"}
	var devices []DeviceInfo

	for _, base := range paths {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			p := filepath.Join(base, entry.Name())
			subEntries, err := os.ReadDir(p)
			if err == nil && len(subEntries) > 0 {
				for _, sub := range subEntries {
					if !sub.IsDir() {
						continue
					}
					sp := filepath.Join(p, sub.Name())
					total, _, err := getDiskUsage(sp)
					if err != nil {
						continue
					}
					devices = append(devices, DeviceInfo{
						Index:     len(devices),
						Name:      sub.Name(),
						Path:      sp,
						Removable: true,
						Size:      total,
					})
				}
			} else {
				total, _, err := getDiskUsage(p)
				if err != nil {
					continue
				}
				devices = append(devices, DeviceInfo{
					Index:     len(devices),
					Name:      entry.Name(),
					Path:      p,
					Removable: true,
					Size:      total,
				})
			}
		}
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
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", fmt.Errorf("无法打开 /proc/mounts: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		device := fields[0]
		mount := fields[1]
		if mount == mountPath {
			return device, nil
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

	var stat syscall.Stat_t
	f, err := os.OpenFile(rawPath, os.O_RDONLY|syscall.O_DIRECT, 0)
	if err != nil {
		f, err = os.OpenFile(rawPath, os.O_RDONLY, 0)
		if err != nil {
			return nil, fmt.Errorf("无法打开原始设备 (需要root权限): %v", err)
		}
	}
	fd := int(f.Fd())
	if err := syscall.Fstat(fd, &stat); err != nil {
		f.Close()
		return nil, fmt.Errorf("无法获取设备信息: %v", err)
	}
	deviceSize := uint64(stat.Size)

	sectorSize := getLinuxSectorSize(rawPath)

	testBytes := deviceSize
	totalSectors := int(testBytes / uint64(sectorSize))

	fmt.Printf("  设备总大小: %s\n", formatBytes(deviceSize))
	fmt.Printf("  测试范围:   %s (%d 个扇区)\n", formatBytes(testBytes), totalSectors)
	fmt.Printf("  扇区大小:   %d bytes\n", sectorSize)

	alignedSize := sectorSize
	if alignedSize < 4096 {
		alignedSize = 4096
	}

	var badReadSectors int32
	var badWriteSectors int32

	fmt.Println("\n  [阶段1/2] 只读扫描...")
	for sec := 0; sec < totalSectors; sec++ {
		offset := int64(sec) * int64(sectorSize)
		readBuf := make([]byte, alignedSize)
		_, err := f.ReadAt(readBuf[:sectorSize], offset)
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
	wf, err := os.OpenFile(rawPath, os.O_RDWR|syscall.O_DIRECT, 0)
	if err != nil {
		wf, err = os.OpenFile(rawPath, os.O_RDWR, 0)
		if err != nil {
			return nil, fmt.Errorf("无法以写入模式打开原始设备: %v", err)
		}
	}
	defer wf.Close()

	writeSectors := totalSectors
	writeStep := 1

	pattern := generateSectorPattern(0, sectorSize)
	originalData := make([]byte, alignedSize)
	verifyBuf := make([]byte, alignedSize)

	for i := 0; i < writeSectors; i++ {
		sec := i * writeStep
		offset := int64(sec) * int64(sectorSize)

		_, err := wf.ReadAt(originalData[:sectorSize], offset)
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

		_, err = wf.ReadAt(verifyBuf[:sectorSize], offset)
		if err != nil {
			atomic.AddInt32(&badWriteSectors, 1)
			fmt.Printf("  ⚠ 扇区 %d 写入后读取失败: %v\n", sec, err)
			wf.WriteAt(originalData[:sectorSize], offset)
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

		wf.WriteAt(originalData[:sectorSize], offset)

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

func getLinuxSectorSize(devicePath string) int {
	baseName := filepath.Base(devicePath)

	parts := strings.Split(baseName, "")
	if len(parts) == 0 {
		return 512
	}

	var diskName string
	for i := 0; i < len(parts); i++ {
		if baseName[i] >= '0' && baseName[i] <= '9' {
			diskName = baseName[:i]
			break
		}
	}
	if diskName == "" {
		diskName = baseName
	}

	path := filepath.Join("/sys/block", diskName, "queue", "physical_block_size")
	data, err := os.ReadFile(path)
	if err == nil {
		size, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil && size > 0 {
			return size
		}
	}

	path = filepath.Join("/sys/block", diskName, "queue", "logical_block_size")
	data, err = os.ReadFile(path)
	if err == nil {
		size, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil && size > 0 {
			return size
		}
	}

	return 512
}

func extractJSONString(line, key string) string {
	keyIdx := strings.Index(line, "\""+key+"\"")
	if keyIdx < 0 {
		return ""
	}
	rest := line[keyIdx+len(key)+3:]
	colonIdx := strings.Index(rest, ":")
	if colonIdx < 0 {
		return ""
	}
	rest = strings.TrimSpace(rest[colonIdx+1:])
	if strings.HasPrefix(rest, "\"") {
		rest = rest[1:]
		quoteIdx := strings.Index(rest, "\"")
		if quoteIdx >= 0 {
			return rest[:quoteIdx]
		}
		return rest
	}
	sepIdx := strings.IndexAny(rest, ",}\n")
	if sepIdx >= 0 {
		return strings.TrimSpace(rest[:sepIdx])
	}
	return strings.TrimSpace(rest)
}

func formatDrive(path string) error {
	device, err := resolveRawDevice(path)
	if err != nil {
		return fmt.Errorf("无法找到设备: %v", err)
	}

	fmt.Printf("正在卸载 %s...\n", path)
	exec.Command("umount", path).Run()

	fmt.Printf("正在格式化 %s (exFAT)...\n", device)
	cmd := exec.Command("mkfs.exfat", device)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		cmd = exec.Command("mkfs.vfat", "-F32", device)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("格式化失败: %v", err)
		}
	}

	fmt.Printf("正在重新挂载 %s...\n", path)
	exec.Command("mount", device, path).Run()

	return nil
}