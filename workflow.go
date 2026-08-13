package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func checkSameDrive(targetPath string) bool {
	exePath, err := getExecutableDrivePath()
	if err != nil {
		return false
	}

	exeAbs, _ := filepath.Abs(exePath)
	targetAbs, _ := filepath.Abs(targetPath)

	var sameDrive bool
	if runtime.GOOS == "windows" {
		if len(exeAbs) >= 2 && len(targetAbs) >= 2 && exeAbs[1] == ':' && targetAbs[1] == ':' {
			sameDrive = strings.EqualFold(exeAbs[:2], targetAbs[:2])
		}
	} else {
		exeAbs = filepath.Clean(exeAbs)
		targetAbs = filepath.Clean(targetAbs)
		sameDrive = strings.HasPrefix(targetAbs, exeAbs+string(os.PathSeparator)) ||
			strings.HasPrefix(exeAbs, targetAbs+string(os.PathSeparator)) ||
			targetAbs == exeAbs
	}

	if sameDrive {
		fmt.Println()
		fmt.Println(strings.Repeat("!", 50))
		fmt.Println("  警告: 不允许对程序自身所在的磁盘进行检测!")
		fmt.Println("  请将本程序放到其他磁盘上运行，然后再检测目标磁盘。")
		fmt.Println(strings.Repeat("!", 50))
	}
	return sameDrive
}

func runAutoTest(path string) {
	capacity, free, err := getDiskUsage(path)
	if err != nil {
		fmt.Printf("无法访问路径 %s: %v\n", path, err)
		fmt.Println("提示: 如需手动选择设备，请使用 -i 参数")
		return
	}

	fmt.Printf("已自动检测到存储设备: %s\n", path)
	fmt.Printf("  总容量: %s\n", formatBytes(capacity))
	fmt.Printf("  可用空间: %s\n", formatBytes(free))

	if checkSameDrive(path) {
		return
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("\n确认开始测试? 数据可能丢失! [y/N]: ")
	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(confirm)) != "y" {
		fmt.Println("已取消")
		return
	}

	runTests(path)

	fmt.Println()
	fmt.Print("是否进行原始扇区测试 (只读扫描，不写入数据)? [y/N]: ")
	rawConfirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(rawConfirm)) == "y" {
		runRawTest(path)
	}
}

func runInteractive() {
	devices, err := listRemovableDevices()
	if err != nil {
		fmt.Printf("获取设备列表失败: %v\n", err)
		manualPath()
		return
	}

	if len(devices) == 0 {
		fmt.Println("未检测到可移动设备，请手动输入路径")
		manualPath()
		return
	}

	fmt.Println("检测到的存储设备:")
	for _, d := range devices {
		sizeStr := formatBytes(d.Size)
		removable := "固定"
		if d.Removable {
			removable = "可移动"
		}
		fmt.Printf("  [%d] %s (%s, %s, %s)\n", d.Index, d.Name, d.Path, sizeStr, removable)
	}
	fmt.Println("  [M] 手动输入路径")

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("\n请选择要测试的设备 [0-" + strconv.Itoa(len(devices)-1) + " 或 M]: ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToUpper(input))

	var targetPath string
	if input == "M" {
		manualPath()
		return
	}

	idx, err := strconv.Atoi(input)
	if err != nil || idx < 0 || idx >= len(devices) {
		fmt.Println("无效选择")
		return
	}
	targetPath = devices[idx].Path

	fmt.Println()
	fmt.Printf("已选择: %s\n", targetPath)

	if checkSameDrive(targetPath) {
		return
	}

	fmt.Print("确认开始测试? 数据可能丢失! [y/N]: ")
	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(confirm)) != "y" {
		fmt.Println("已取消")
		return
	}

	runTests(targetPath)

	fmt.Println()
	fmt.Print("是否进行原始扇区测试 (只读扫描，不写入数据)? [y/N]: ")
	rawConfirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(rawConfirm)) == "y" {
		runRawTest(targetPath)
	}
}

func manualPath() {
	reader := bufio.NewReader(os.Stdin)
	var defaultPath string
	switch runtime.GOOS {
	case "windows":
		defaultPath = "E:\\"
	case "darwin":
		defaultPath = "/Volumes/UDISK"
	default:
		defaultPath = "/media/user/UDISK"
	}
	fmt.Printf("请输入SD卡挂载路径 (默认: %s): ", defaultPath)
	path, _ := reader.ReadString('\n')
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultPath
	}
	if path == "" {
		return
	}
	if checkSameDrive(path) {
		return
	}
	runTests(path)

	fmt.Println()
	fmt.Print("是否进行原始扇区测试 (只读扫描，不写入数据)? [y/N]: ")
	reader2 := bufio.NewReader(os.Stdin)
	rawConfirm, _ := reader2.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(rawConfirm)) == "y" {
		runRawTest(path)
	}
}