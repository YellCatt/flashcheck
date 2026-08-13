package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
	fmt.Printf("读取损坏:    %d / %d (%.2f%%)\n", r.BadReadSectors, r.SectorsTested, float64(r.BadReadSectors)/float64(max(1, r.SectorsTested))*100)
	fmt.Printf("写入损坏:    %d / %d (%.2f%%)\n", r.BadWriteSectors, r.SectorsTested, float64(r.BadWriteSectors)/float64(max(1, r.SectorsTested))*100)
	fmt.Printf("扫描方式:    %s\n", r.PatternDesc)
	fmt.Println(strings.Repeat("=", 50))

	totalBad := r.BadReadSectors + r.BadWriteSectors
	if totalBad == 0 {
		fmt.Println("结论: 原始扇区测试通过，硬件层面无损坏")
	} else if float64(totalBad)/float64(max(1, r.SectorsTested)) < 0.01 {
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