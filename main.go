package main

import (
	"fmt"
	"os"
	"strings"
)

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
		runInteractive()
	}
}

func printUsage() {
	fmt.Printf("用法: %s [选项]\n", os.Args[0])
	fmt.Println()
	fmt.Println("选项:")
	fmt.Println("  (无参数)         交互模式，列出可移动设备供选择")
	fmt.Println("  -i, --interactive 交互模式，手动选择设备")
	fmt.Println("  -path <路径>     指定要测试的路径")
	fmt.Println("  -h, --help       显示帮助信息")
}