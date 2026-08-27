package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
)

// ============================================================
// Qbot 基本信息
// ============================================================

const (
	QbotName    = "Qbot"
	QbotVersion = "0.1.0"
)

const (
	AppName    = "Qbot"
	AppVersion = "0.1.0"
	AppAuthor  = "Ma-Chenwei"
)

// ============================================================
// Qbot.go
//
// Qbot 启动入口
//
// 文件结构：
//
//	Qbot.go   启动入口 / 编译信息
//	init.go   配置 / Memory / 初始化
//	main.go   核心运行生命周期
//	web.go    WebUI / iOS 3 风格控制面板
//	ai.go     OpenAI Compatible AI
//
// 注意：
// main() 位于 main.go。
// Qbot.go 不再重复定义 main()。
// ============================================================

func init() {
	// 确保程序以 Qbot 身份运行。
}

// ============================================================
// BuildInfo
// ============================================================

type BuildInfo struct {
	Name      string
	Version   string
	GoVersion string
	OS        string
	Arch      string
}

// ============================================================
// GetBuildInfo
// ============================================================

func GetBuildInfo() BuildInfo {
	return BuildInfo{
		Name:      AppName,
		Version:   AppVersion,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

// ============================================================
// PrintVersion
// ============================================================

func PrintVersion() {
	info := GetBuildInfo()

	fmt.Println()
	fmt.Println("Qbot")
	fmt.Println("======================================")
	fmt.Println("Version :", info.Version)
	fmt.Println("Go      :", info.GoVersion)
	fmt.Println("OS      :", info.OS)
	fmt.Println("Arch    :", info.Arch)
	fmt.Println("======================================")
	fmt.Println()
}

// ============================================================
// PrintHelp
// ============================================================

func PrintHelp() {
	fmt.Println(`
Qbot - QQ Third-Party Client

用法：

    Qbot.exe
        正常启动 Qbot

    Qbot.exe --version
        查看版本

    Qbot.exe --help
        查看帮助

    Qbot.exe --config
        显示配置文件位置

    Qbot.exe --web
        启动 Web 控制面板

配置：

    config.json

WebUI：

    默认：
    http://127.0.0.1:8080

AI：

    OpenAI Compatible API

支持：

    OpenAI
    OpenRouter
    DeepSeek
    SiliconFlow
    Ollama
    LM Studio
    OneAPI
    NewAPI
    其他 OpenAI Compatible 服务

所有 AI 配置均可以通过 WebUI 修改。
`)
}

// ============================================================
// PrintConfigPath
// ============================================================

func PrintConfigPath() {

	path, err := os.Getwd()

	if err != nil {
		fmt.Println(
			"当前目录:",
			".",
		)

		return
	}

	fmt.Println(
		"Config:",
		path+"/config.json",
	)
}

// ============================================================
// ParseQbotFlags
// ============================================================
//
// 返回：
//   runWebOnly
//
// 注意：
// 默认情况下仍然由 main.go 正常启动。
// ============================================================

func ParseQbotFlags() bool {

	versionFlag :=
		flag.Bool(
			"version",
			false,
			"显示 Qbot 版本",
		)

	helpFlag :=
		flag.Bool(
			"help",
			false,
			"显示帮助",
		)

	configFlag :=
		flag.Bool(
			"config",
			false,
			"显示配置文件位置",
		)

	webFlag :=
		flag.Bool(
			"web",
			false,
			"启动 Web 控制面板",
		)

	flag.Parse()

	// --------------------------------------------------------
	// version
	// --------------------------------------------------------

	if *versionFlag {

		PrintVersion()

		os.Exit(0)
	}

	// --------------------------------------------------------
	// help
	// --------------------------------------------------------

	if *helpFlag {

		PrintHelp()

		os.Exit(0)
	}

	// --------------------------------------------------------
	// config
	// --------------------------------------------------------

	if *configFlag {

		PrintConfigPath()

		os.Exit(0)
	}

	// --------------------------------------------------------
	// web
	// --------------------------------------------------------

	return *webFlag
}

// ============================================================
// StartupBanner
// ============================================================

func StartupBanner() {

	info :=
		GetBuildInfo()

	fmt.Println(
		"======================================",
	)

	fmt.Println(
		" Qbot",
	)

	fmt.Println(
		" QQ Third-Party Client",
	)

	fmt.Println(
		"--------------------------------------",
	)

	fmt.Printf(
		" Version : %s\n",
		info.Version,
	)

	fmt.Printf(
		" Runtime : %s\n",
		info.GoVersion,
	)

	fmt.Printf(
		" Platform: %s/%s\n",
		info.OS,
		info.Arch,
	)

	fmt.Println(
		" AI      : OpenAI Compatible",
	)

	fmt.Println(
		" WebUI   : iOS 3 Style",
	)

	fmt.Println(
		"======================================",
	)

	fmt.Println()
}

// ============================================================
// init.go / main.go 调用入口
// ============================================================
//
// Go 会自动执行所有文件中的 init()，
// main() 则由 main.go 负责。
//
// 为了避免：
//
//     Qbot.go
//     main.go
//
// 同时定义 main()，这里不定义 main。
//
// ============================================================