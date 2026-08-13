package main

import (
	"bufio"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func runTests(path string) {
	start := time.Now()
	result := TestResult{Device: path}

	fmt.Println()
	fmt.Println(strings.Repeat("!", 50))
	fmt.Println("  即将格式化目标磁盘!")
	fmt.Println("  所有数据将被清除，此操作不可逆!")
	fmt.Println(strings.Repeat("!", 50))

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("请输入 YES 确认格式化 (其他任意键跳过格式化，直接测试): ")
	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToUpper(confirm)) == "YES" {
		if err := formatDrive(path); err != nil {
			fmt.Printf("  ✗ 格式化失败: %v\n", err)
			fmt.Println("  继续当前文件系统测试...")
		} else {
			fmt.Println("  ✓ 格式化完成")
		}
	} else {
		fmt.Println("  跳过格式化，直接在当前文件系统上进行测试")
	}

	fmt.Println("\n[1/5] 检测容量与写入权限...")
	capacity, free, err := getDiskUsage(path)
	if err != nil {
		fmt.Printf("  ✗ 无法访问: %v\n", err)
		return
	}
	result.Capacity = capacity
	fmt.Printf("  总容量: %s\n", formatBytes(capacity))
	fmt.Printf("  可用空间: %s\n", formatBytes(free))
	testSize := free * 8 / 10
	fmt.Printf("  全盘测试数据量: %s (约占可用空间 80%%)\n", formatBytes(testSize))

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

	fmt.Println("\n[5/5] 坏块全盘扫描...")
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

	_, free, err := getDiskUsage(path)
	if err != nil {
		fmt.Printf("  ✗ 无法获取磁盘信息: %v\n", err)
		return 0, 0
	}
	testSize := free * 8 / 10
	if testSize < 64*1024*1024 {
		testSize = 64 * 1024 * 1024
	}

	chunkSize := 64 * 1024 * 1024
	chunk := make([]byte, chunkSize)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	_, _ = rng.Read(chunk)

	start := time.Now()
	f, err := os.OpenFile(testFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Printf("  ✗ 无法创建测试文件: %v\n", err)
		return 0, 0
	}

	remain := uint64(0)
	for remain < testSize {
		n := uint64(chunkSize)
		if testSize-remain < n {
			n = testSize - remain
		}
		_, err := f.Write(chunk[:n])
		if err != nil {
			fmt.Printf("  ✗ 写入失败: %v\n", err)
			f.Close()
			return 0, 0
		}
		remain += n
	}
	f.Sync()
	f.Close()
	write = float64(testSize) / 1024 / 1024 / time.Since(start).Seconds()

	start = time.Now()
	fs, err := openFileDirectRead(testFile)
	if err != nil {
		fmt.Printf("  ✗ 直接读取打开失败: %v\n", err)
		return write, 0
	}
	defer fs.Close()

	readChunkSize := 4 * 1024 * 1024
	remain = 0
	buf := alignedBuffer(readChunkSize, 4096)
	for remain < testSize {
		n := readChunkSize
		if testSize-remain < uint64(n) {
			n = int(testSize - remain)
		}
		_, err := io.ReadFull(fs, buf[:n])
		if err != nil {
			fmt.Printf("  ✗ 直接读取失败: %v\n", err)
			return write, 0
		}
		remain += uint64(n)
	}
	read = float64(testSize) / 1024 / 1024 / time.Since(start).Seconds()

	return write, read
}

func testRandom(path string) (write, read float64) {
	testFile := filepath.Join(path, testFileName+"_rand")
	randBlockSize := 4 * 1024

	_, free, err := getDiskUsage(path)
	if err != nil {
		fmt.Printf("  ✗ 无法获取磁盘信息: %v\n", err)
		return 0, 0
	}
	size := free * 8 / 10
	if size < 64*1024*1024 {
		size = 64 * 1024 * 1024
	}

	f, err := os.OpenFile(testFile, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Printf("  ✗ 无法创建测试文件: %v\n", err)
		return 0, 0
	}
	defer os.Remove(testFile)
	f.Truncate(int64(size))

	count := int(size / uint64(randBlockSize) / 100)
	if count < 1000 {
		count = 1000
	}
	if count > 50000 {
		count = 50000
	}

	positions := make([]int64, count)
	for i := range positions {
		positions[i] = int64(randIntn(int(size / uint64(randBlockSize)))) * int64(randBlockSize)
	}
	block := make([]byte, randBlockSize)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	_, _ = rng.Read(block)

	start := time.Now()
	for _, pos := range positions {
		f.WriteAt(block, pos)
	}
	f.Sync()
	f.Close()
	write = float64(count*randBlockSize) / 1024 / 1024 / time.Since(start).Seconds()

	fs, err := openFileDirectRead(testFile)
	if err != nil {
		fmt.Printf("  ✗ 直接读取打开失败: %v\n", err)
		return write, 0
	}
	defer fs.Close()

	readBuf := alignedBuffer(randBlockSize, 4096)

	start = time.Now()
	for _, pos := range positions {
		_, err := fs.ReadAt(readBuf[:randBlockSize], pos)
		if err != nil {
			fmt.Printf("  ✗ 直接随机读取失败: %v\n", err)
			fs.Close()
			return write, 0
		}
	}
	read = float64(count*randBlockSize) / 1024 / 1024 / time.Since(start).Seconds()

	fs.Close()
	return write, read
}

func testIntegrity(path string) bool {
	testFile := filepath.Join(path, testFileName+"_integrity")

	_, free, err := getDiskUsage(path)
	if err != nil {
		fmt.Printf("  ✗ 无法获取磁盘信息: %v\n", err)
		return false
	}
	size := free * 8 / 10
	if size < 10*1024*1024 {
		size = 10 * 1024 * 1024
	}

	seed := uint32(time.Now().UnixNano())
	chunkSize := 64 * 1024 * 1024

	f, err := os.OpenFile(testFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Printf("  ✗ 无法创建测试文件: %v\n", err)
		return false
	}

	writeCRC := crc32.NewIEEE()
	remain := uint64(0)
	for remain < size {
		n := uint64(chunkSize)
		if size-remain < n {
			n = size - remain
		}
		data := generateSeededData(int(n), seed+uint32(remain))
		f.Write(data)
		writeCRC.Write(data)
		remain += n
	}
	f.Close()

	f, err = os.Open(testFile)
	if err != nil {
		return false
	}

	readCRC := crc32.NewIEEE()
	readBuf := make([]byte, chunkSize)
	remain = 0
	for remain < size {
		n := chunkSize
		if size-remain < uint64(n) {
			n = int(size - remain)
		}
		_, err := io.ReadFull(f, readBuf[:n])
		if err != nil {
			f.Close()
			return false
		}
		readCRC.Write(readBuf[:n])
		remain += uint64(n)
	}
	f.Close()

	os.Remove(testFile)
	return writeCRC.Sum32() == readCRC.Sum32()
}

func testBadBlocks(path string) (bad, total int) {
	testFile := filepath.Join(path, testFileName+"_badblock")

	_, free, err := getDiskUsage(path)
	if err != nil {
		return 0, 0
	}

	testSize := free * 8 / 10
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
	fmt.Println("  警告: 原始扇区测试将直接读写硬件扇区!")
	fmt.Println("  测试分为两步:")
	fmt.Println("    1. 只读扫描 - 检测所有扇区是否可读")
	fmt.Println("    2. 写入测试 - 写入测试数据后读回验证，最后恢复原数据")
	fmt.Println("  需要管理员/root权限访问原始设备。")
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