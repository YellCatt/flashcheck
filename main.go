package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	version       = "1.0.0"
	testFileName  = ".sd_test_tmp"
	blockSize     = 4 * 1024 * 1024
	samplePercent = 10
)

type DeviceInfo struct {
	Index      int
	Name       string
	Path       string
	Removable  bool
	Size       uint64
	Filesystem string
}

type TestResult struct {
	Device       string
	Capacity     uint64
	SeqWrite     float64
	SeqRead      float64
	RandWrite    float64
	RandRead     float64
	Integrity    bool
	BadBlocks    int
	TotalBlocks  int
	TestDuration time.Duration
}

type RawTestResult struct {
	Device        string
	RawPath       string
	SectorsTested int
	BadSectors    int
	SectorSize    int
	TestDuration  time.Duration
	PatternDesc   string
}

func main() {
	fmt.Printf("=== SD Card Tester v%s ===\n", version)
	fmt.Println("警告: 测试将写入临时文件，请确保卡内数据已备份！")
	fmt.Println()

	mode := "auto"
	customPath := ""

	for _, arg := range os.Args[1:] {
		if arg == "-i" || arg == "--interactive" {
			mode = "interactive"
		} else if arg == "-h" || arg == "--help" {
			printUsage()
			return
		} else if strings.HasPrefix(arg, "-path=") {
			customPath = strings.TrimPrefix(arg, "-path=")
			mode = "custom"
		} else if strings.HasPrefix(arg, "--path=") {
			customPath = strings.TrimPrefix(arg, "--path=")
			mode = "custom"
		}
	}

	switch mode {
	case "interactive":
		runInteractive()
	case "custom":
		if customPath == "" {
			fmt.Println("错误: 未指定路径")
			printUsage()
			return
		}
		runAutoTest(customPath)
	default:
		exePath, err := getExecutableDrivePath()
		if err != nil {
			fmt.Printf("自动检测失败: %v\n", err)
			fmt.Println("回退到交互模式...")
			runInteractive()
			return
		}
		runAutoTest(exePath)
	}
}

func printUsage() {
	fmt.Printf("用法: %s [选项]\n", os.Args[0])
	fmt.Println()
	fmt.Println("选项:")
	fmt.Println("  (无参数)         自动检测当前可执行文件所在的存储设备并测试")
	fmt.Println("  -i, --interactive 交互模式，手动选择设备")
	fmt.Println("  -path <路径>     指定要测试的路径")
	fmt.Println("  -h, --help       显示帮助信息")
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

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("\n确认开始测试? 数据可能丢失! [y/N]: ")
	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(confirm)) != "y" {
		fmt.Println("已取消")
		return
	}

	runTests(path)

	fmt.Println()
	fmt.Print("是否进行原始扇区测试 (直接操作硬件，DANGER!)? [y/N]: ")
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

	fmt.Print("确认开始测试? 数据可能丢失! [y/N]: ")
	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(confirm)) != "y" {
		fmt.Println("已取消")
		return
	}

	runTests(targetPath)

	fmt.Println()
	fmt.Print("是否进行原始扇区测试 (直接操作硬件，DANGER!)? [y/N]: ")
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
	runTests(path)

	fmt.Println()
	fmt.Print("是否进行原始扇区测试 (直接操作硬件，DANGER!)? [y/N]: ")
	reader2 := bufio.NewReader(os.Stdin)
	rawConfirm, _ := reader2.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(rawConfirm)) == "y" {
		runRawTest(path)
	}
}

func runTests(path string) {
	start := time.Now()
	result := TestResult{Device: path}

	fmt.Println("\n[1/5] 检测容量与写入权限...")
	capacity, free, err := getDiskUsage(path)
	if err != nil {
		fmt.Printf("  ✗ 无法访问: %v\n", err)
		return
	}
	result.Capacity = capacity
	fmt.Printf("  总容量: %s\n", formatBytes(capacity))
	fmt.Printf("  可用空间: %s\n", formatBytes(free))

	fmt.Println("\n[2/5] 顺序读写速度测试...")
	seqWrite, seqRead := testSequential(path)
	result.SeqWrite = seqWrite
	result.SeqRead = seqRead
	fmt.Printf("  顺序写入: %.2f MB/s\n", seqWrite)
	fmt.Printf("  顺序读取: %.2f MB/s\n", seqRead)

	fmt.Println("\n[3/5] 随机读写速度测试...")
	randWrite, randRead := testRandom(path)
	result.RandWrite = randWrite
	result.RandRead = randRead
	fmt.Printf("  随机写入: %.2f MB/s\n", randWrite)
	fmt.Printf("  随机读取: %.2f MB/s\n", randRead)

	fmt.Println("\n[4/5] 数据完整性测试...")
	integrity := testIntegrity(path)
	result.Integrity = integrity
	if integrity {
		fmt.Println("  ✓ 数据完整性验证通过")
	} else {
		fmt.Println("  ✗ 数据完整性验证失败！卡片可能损坏或为扩容卡")
	}

	fmt.Println("\n[5/5] 坏块抽样扫描...")
	bad, total := testBadBlocks(path)
	result.BadBlocks = bad
	result.TotalBlocks = total
	if bad == 0 {
		fmt.Printf("  ✓ 扫描完成，未发现坏块 (共%d块)\n", total)
	} else {
		fmt.Printf("  ✗ 发现 %d 个坏块 / 共 %d 块 (%.2f%%)\n", bad, total, float64(bad)/float64(total)*100)
	}

	cleanup(path)
	result.TestDuration = time.Since(start)

	printReport(&result)
}

func testSequential(path string) (write, read float64) {
	testFile := filepath.Join(path, testFileName)
	data := make([]byte, 64*1024*1024)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	_, _ = rng.Read(data)

	start := time.Now()
	f, err := os.OpenFile(testFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Printf("  ✗ 无法创建测试文件: %v\n", err)
		return 0, 0
	}
	_, err = f.Write(data)
	f.Close()
	if err != nil {
		fmt.Printf("  ✗ 写入失败: %v\n", err)
		return 0, 0
	}
	write = float64(len(data)) / 1024 / 1024 / time.Since(start).Seconds()

	start = time.Now()
	f, err = os.Open(testFile)
	if err != nil {
		return write, 0
	}
	buf := make([]byte, len(data))
	_, err = io.ReadFull(f, buf)
	f.Close()
	if err != nil {
		return write, 0
	}
	read = float64(len(data)) / 1024 / 1024 / time.Since(start).Seconds()

	return write, read
}

func testRandom(path string) (write, read float64) {
	testFile := filepath.Join(path, testFileName+"_rand")
	size := 16 * 1024 * 1024
	blockSize := 4 * 1024

	f, err := os.OpenFile(testFile, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		fmt.Printf("  ✗ 无法创建测试文件: %v\n", err)
		return 0, 0
	}
	defer os.Remove(testFile)
	f.Truncate(int64(size))

	count := 1000
	positions := make([]int64, count)
	for i := range positions {
		positions[i] = int64(randIntn(size/blockSize)) * int64(blockSize)
	}
	block := make([]byte, blockSize)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	_, _ = rng.Read(block)

	start := time.Now()
	for _, pos := range positions {
		f.WriteAt(block, pos)
	}
	f.Sync()
	write = float64(count*blockSize) / 1024 / 1024 / time.Since(start).Seconds()

	readBuf := make([]byte, blockSize)
	start = time.Now()
	for _, pos := range positions {
		f.ReadAt(readBuf, pos)
	}
	read = float64(count*blockSize) / 1024 / 1024 / time.Since(start).Seconds()

	f.Close()
	return write, read
}

func testIntegrity(path string) bool {
	testFile := filepath.Join(path, testFileName+"_integrity")
	size := 10 * 1024 * 1024

	seed := uint32(time.Now().UnixNano())
	data := generateSeededData(size, seed)

	f, err := os.OpenFile(testFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Printf("  ✗ 无法创建测试文件: %v\n", err)
		return false
	}
	_, err = f.Write(data)
	f.Close()
	if err != nil {
		return false
	}

	f, err = os.Open(testFile)
	if err != nil {
		return false
	}
	readData := make([]byte, size)
	_, err = io.ReadFull(f, readData)
	f.Close()
	if err != nil {
		return false
	}

	writeCRC := crc32.ChecksumIEEE(data)
	readCRC := crc32.ChecksumIEEE(readData)

	os.Remove(testFile)
	return writeCRC == readCRC
}

func testBadBlocks(path string) (bad, total int) {
	testFile := filepath.Join(path, testFileName+"_badblock")

	_, free, err := getDiskUsage(path)
	if err != nil {
		return 0, 0
	}

	testSize := free * 8 / 10
	if testSize > 500*1024*1024 {
		testSize = 500 * 1024 * 1024
	}
	if testSize < blockSize {
		return 0, 0
	}

	total = int(testSize / blockSize)
	sampleCount := total * samplePercent / 100
	if sampleCount < 1 {
		sampleCount = total
	}

	block := make([]byte, blockSize)
	for i := range block {
		block[i] = byte(i % 256)
	}

	f, err := os.OpenFile(testFile, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return 0, total
	}
	defer os.Remove(testFile)
	f.Truncate(int64(testSize))

	indices := randPerm(total)[:sampleCount]
	sort.Ints(indices)

	var badCount int32
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 4)

	for _, idx := range indices {
		wg.Add(1)
		semaphore <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-semaphore }()

			offset := int64(i) * int64(blockSize)

			_, err := f.WriteAt(block, offset)
			if err != nil {
				atomic.AddInt32(&badCount, 1)
				return
			}

			readBuf := make([]byte, blockSize)
			_, err = f.ReadAt(readBuf, offset)
			if err != nil {
				atomic.AddInt32(&badCount, 1)
				return
			}

			for j := 0; j < len(block); j++ {
				if readBuf[j] != block[j] {
					atomic.AddInt32(&badCount, 1)
					return
				}
			}
		}(idx)
	}

	wg.Wait()
	f.Close()
	return int(badCount), total
}

func runRawTest(mountPath string) {
	fmt.Println()
	fmt.Println(strings.Repeat("!", 50))
	fmt.Println("  警告: 原始扇区测试将直接写入硬件!")
	fmt.Println("  这将破坏设备上的所有数据，且不可恢复!")
	fmt.Println("  请确保已选择正确的设备！")
	fmt.Println(strings.Repeat("!", 50))

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("\n输入 YES 确认执行 (其他任意键取消): ")
	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToUpper(confirm)) != "YES" {
		fmt.Println("已取消原始扇区测试")
		return
	}

	fmt.Println("\n[原始扇区测试] 正在解析设备路径...")
	rawPath, err := resolveRawDevice(mountPath)
	if err != nil {
		fmt.Printf("  ✗ 无法解析原始设备路径: %v\n", err)
		fmt.Println("  提示: 在Linux/macOS上可能需要root权限")
		return
	}
	fmt.Printf("  原始设备路径: %s\n", rawPath)

	fmt.Println("\n[原始扇区测试] 开始扫描 (这可能需要较长时间)...")
	result, err := rawSectorTest(mountPath)
	if err != nil {
		fmt.Printf("  ✗ 原始扇区测试失败: %v\n", err)
		return
	}

	printRawReport(result)
}

func printReport(r *TestResult) {
	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("           SD 卡测试报告")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("设备路径:    %s\n", r.Device)
	fmt.Printf("标称容量:    %s\n", formatBytes(r.Capacity))
	fmt.Printf("测试耗时:    %s\n", r.TestDuration.Round(time.Second))
	fmt.Println(strings.Repeat("-", 50))
	fmt.Println("性能测试:")
	fmt.Printf("  顺序写入:  %.2f MB/s\n", r.SeqWrite)
	fmt.Printf("  顺序读取:  %.2f MB/s\n", r.SeqRead)
	fmt.Printf("  随机写入:  %.2f MB/s\n", r.RandWrite)
	fmt.Printf("  随机读取:  %.2f MB/s\n", r.RandRead)
	fmt.Println(strings.Repeat("-", 50))
	fmt.Println("可靠性测试:")
	if r.Integrity {
		fmt.Println("  数据完整性: 通过")
	} else {
		fmt.Println("  数据完整性: 失败")
	}
	fmt.Printf("  坏块扫描:   %d/%d (%.2f%%)\n", r.BadBlocks, r.TotalBlocks, float64(r.BadBlocks)/float64(max(1, r.TotalBlocks))*100)
	fmt.Println(strings.Repeat("=", 50))

	score := 0
	if r.SeqRead > 10 {
		score += 20
	}
	if r.SeqWrite > 5 {
		score += 20
	}
	if r.Integrity {
		score += 30
	}
	if r.BadBlocks == 0 {
		score += 30
	}

	fmt.Print("综合评级: ")
	switch {
	case score >= 90:
		fmt.Println("A (优秀，可放心使用)")
	case score >= 70:
		fmt.Println("B (良好，适合日常使用)")
	case score >= 50:
		fmt.Println("C (一般，建议仅存储非重要数据)")
	default:
		fmt.Println("D (较差，建议更换)")
	}
}

func printRawReport(r *RawTestResult) {
	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("        原始扇区测试报告")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("挂载路径:    %s\n", r.Device)
	fmt.Printf("原始设备:    %s\n", r.RawPath)
	fmt.Printf("扇区大小:    %d bytes\n", r.SectorSize)
	fmt.Printf("测试扇区数:  %d\n", r.SectorsTested)
	fmt.Printf("耗时:        %s\n", r.TestDuration.Round(time.Second))
	fmt.Println(strings.Repeat("-", 50))
	fmt.Printf("损坏扇区:    %d / %d (%.2f%%)\n", r.BadSectors, r.SectorsTested, float64(r.BadSectors)/float64(max(1, r.SectorsTested))*100)
	fmt.Printf("写入模式:    %s\n", r.PatternDesc)
	fmt.Println(strings.Repeat("=", 50))

	if r.BadSectors == 0 {
		fmt.Println("结论: 原始扇区测试通过，硬件层面无损坏")
	} else if float64(r.BadSectors)/float64(max(1, r.SectorsTested)) < 0.01 {
		fmt.Println("结论: 少量坏扇区，可能是正常磨损，建议备份重要数据")
	} else {
		fmt.Println("结论: 大量坏扇区，硬件严重损坏或为扩容假卡，建议立即更换")
	}
}

func cleanup(path string) {
	files := []string{testFileName, testFileName + "_rand", testFileName + "_integrity", testFileName + "_badblock"}
	for _, f := range files {
		os.Remove(filepath.Join(path, f))
	}
	fmt.Println("\n已清理临时文件")
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func generateSeededData(size int, seed uint32) []byte {
	data := make([]byte, size)
	for i := 0; i < size; i += 4 {
		binary.LittleEndian.PutUint32(data[i:i+4], seed+uint32(i))
	}
	return data
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func randIntn(n int) int {
	if n <= 0 {
		return 0
	}
	return rand.Intn(n)
}

func randPerm(n int) []int {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		p[i], p[j] = p[j], p[i]
	}
	return p
}

func generateSectorPattern(sectorIndex, sectorSize int) []byte {
	pattern := make([]byte, sectorSize)
	for i := 0; i < sectorSize; i++ {
		pattern[i] = byte((sectorIndex*sectorSize + i) % 256)
	}
	return pattern
}
