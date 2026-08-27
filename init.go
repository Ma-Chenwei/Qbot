package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ============================================================
// Qbot init.go
//
// 负责：
//   - Qbot 全局配置
//   - QQ 配置
//   - AI 配置
//   - Group AI 配置
//   - Memory 配置
//   - Web 配置
//   - Game 配置
//   - config.json 加载 / 保存
//
// 注意：
// Memory 的具体数据结构和操作函数由 ai.go 负责。
// init.go 只负责 MemoryConfig。
// ============================================================

const (
	ConfigFile        = "config.json"
	MemoryDefaultFile = "memory.json"
)

var configMu sync.RWMutex

var QbotConfig Config

// ============================================================
// Config
// ============================================================

type Config struct {
	Name    string `json:"name"`
	Version string `json:"version"`

	QQ QQConfig `json:"qq"`

	AI AIConfig `json:"ai"`

	GroupAI GroupAIConfig `json:"group_ai"`

	Memory MemoryConfig `json:"memory"`

	Web WebConfig `json:"web"`

	Game GameConfig `json:"game"`
}

// ============================================================
// QQConfig
// ============================================================

type QQConfig struct {
	Enabled bool `json:"enabled"`

	Provider string `json:"provider"`

	Account string `json:"account"`

	ClientID string `json:"client_id"`

	ClientSecret string `json:"client_secret"`

	AccessToken string `json:"access_token"`

	AppID string `json:"app_id"`

	BotToken string `json:"bot_token"`

	AutoReconnect bool `json:"auto_reconnect"`

	ReconnectInterval int `json:"reconnect_interval"`

	Debug bool `json:"debug"`
}

// ============================================================
// AIConfig
//
// OpenAI Compatible API
//
// 支持：
//   OpenAI
//   OpenRouter
//   DeepSeek
//   SiliconFlow
//   Ollama
//   LM Studio
//   OneAPI
//   NewAPI
//   其他兼容 /v1/chat/completions 的服务
// ============================================================

type AIConfig struct {
	Enabled bool `json:"enabled"`

	// --------------------------------------------------------
	// API
	// --------------------------------------------------------

	BaseURL string `json:"base_url"`

	APIKey string `json:"api_key"`

	Model string `json:"model"`

	// --------------------------------------------------------
	// Generation
	// --------------------------------------------------------

	Temperature float64 `json:"temperature"`

	MaxTokens int `json:"max_tokens"`

	TopP float64 `json:"top_p"`

	// --------------------------------------------------------
	// System Prompt
	// --------------------------------------------------------

	SystemPrompt string `json:"system_prompt"`

	// --------------------------------------------------------
	// Memory
	// --------------------------------------------------------

	MemoryEnabled bool `json:"memory_enabled"`

	MemoryLimit int `json:"memory_limit"`

	// --------------------------------------------------------
	// Request
	// --------------------------------------------------------

	Timeout int `json:"timeout"`

	Stream bool `json:"stream"`

	// --------------------------------------------------------
	// 自动回复
	// --------------------------------------------------------

	AutoReply bool `json:"auto_reply"`

	ReplyPrefix string `json:"reply_prefix"`
}

// ============================================================
// GroupAIConfig
//
// QQ 群 AI 自动回复配置。
// ============================================================

type GroupAIConfig struct {
	Enabled bool `json:"enabled"`

	// 是否必须 @机器人
	RequireAt bool `json:"require_at"`

	// 是否允许自动回复
	AutoReply bool `json:"auto_reply"`

	// 随机自动回复概率
	Probability float64 `json:"probability"`

	// 两次自动回复之间的冷却时间
	Cooldown int `json:"cooldown"`

	// 每小时最大自动回复数量
	MaxRepliesPerHour int `json:"max_replies_per_hour"`
}

// ============================================================
// MemoryConfig
//
// 这里只保存 Memory 的配置。
// MemoryItem / memoryData / AddMemory 等由 ai.go 负责。
// ============================================================

type MemoryConfig struct {
	Enabled bool `json:"enabled"`

	File string `json:"file"`

	MaxMessages int `json:"max_messages"`

	SaveInterval int `json:"save_interval"`
}

// ============================================================
// WebConfig
// ============================================================

type WebConfig struct {
	Enabled bool `json:"enabled"`

	Host string `json:"host"`

	Port int `json:"port"`

	EnableWebSocket bool `json:"websocket"`

	AutoOpen bool `json:"auto_open"`
}

// ============================================================
// GameConfig
// ============================================================

type GameConfig struct {
	Enabled bool `json:"enabled"`

	GuessNumber bool `json:"guess_number"`

	GuessWord bool `json:"guess_word"`

	Cooldown int `json:"cooldown"`
}

// ============================================================
// DefaultConfig
// ============================================================

func DefaultConfig() Config {
	return Config{
		Name: QbotName,

		Version: QbotVersion,

		// ----------------------------------------------------
		// QQ
		// ----------------------------------------------------

		QQ: QQConfig{
			Enabled: true,

			Provider: "qq-official",

			Account: "",

			ClientID: "",

			ClientSecret: "",

			AccessToken: "",

			AppID: "",

			BotToken: "",

			AutoReconnect: true,

			ReconnectInterval: 5,

			Debug: false,
		},

		// ----------------------------------------------------
		// AI
		// ----------------------------------------------------

		AI: AIConfig{
			Enabled: false,

			BaseURL:
				"https://api.openai.com/v1",

			APIKey: "",

			Model:
				"gpt-4o-mini",

			Temperature:
				0.7,

			MaxTokens:
				1200,

			TopP:
				1.0,

			SystemPrompt:
				"你是 Qbot，一个运行在 QQ 中的 AI 助手。请自然、简洁地回答用户的问题。",

			MemoryEnabled:
				true,

			MemoryLimit:
				20,

			Timeout:
				60,

			Stream:
				false,

			AutoReply:
				true,

			ReplyPrefix:
				"",
		},

		// ----------------------------------------------------
		// Group AI
		// ----------------------------------------------------

		GroupAI: GroupAIConfig{
			Enabled: true,

			RequireAt: true,

			AutoReply: true,

			Probability: 0.35,

			Cooldown: 10,

			MaxRepliesPerHour: 30,
		},

		// ----------------------------------------------------
		// Memory
		// ----------------------------------------------------

		Memory: MemoryConfig{
			Enabled: true,

			File:
				MemoryDefaultFile,

			MaxMessages:
				1000,

			SaveInterval:
				30,
		},

		// ----------------------------------------------------
		// Web
		// ----------------------------------------------------

		Web: WebConfig{
			Enabled: true,

			Host:
				"127.0.0.1",

			Port:
				8080,

			EnableWebSocket:
				false,

			AutoOpen:
				false,
		},

		// ----------------------------------------------------
		// Game
		// ----------------------------------------------------

		Game: GameConfig{
			Enabled: true,

			GuessNumber: true,

			GuessWord: true,

			Cooldown: 3,
		},
	}
}

// ============================================================
// LoadQbotConfig
// ============================================================

func LoadQbotConfig() error {
	configMu.Lock()
	defer configMu.Unlock()

	defaultConfig := DefaultConfig()

	// --------------------------------------------------------
	// 读取 config.json
	// --------------------------------------------------------

	data, err := os.ReadFile(ConfigFile)

	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			QbotConfig = defaultConfig

			if err := saveConfigLocked(
				ConfigFile,
				QbotConfig,
			); err != nil {
				return fmt.Errorf(
					"创建默认配置失败: %w",
					err,
				)
			}

			fmt.Println(
				"[Config] 已创建默认 config.json",
			)

			return nil
		}

		return fmt.Errorf(
			"读取 config.json 失败: %w",
			err,
		)
	}

	// --------------------------------------------------------
	// 解析 JSON
	// --------------------------------------------------------

	var cfg Config

	if err := json.Unmarshal(
		data,
		&cfg,
	); err != nil {
		return fmt.Errorf(
			"解析 config.json 失败: %w",
			err,
		)
	}

	// --------------------------------------------------------
	// 补齐旧配置
	// --------------------------------------------------------

	mergeDefaultConfig(
		&cfg,
		defaultConfig,
	)

	QbotConfig = cfg

	fmt.Println(
		"[Config] config.json 已加载",
	)

	return nil
}

// ============================================================
// SaveConfig
// ============================================================

func SaveConfig(
	filename string,
	cfg Config,
) error {
	configMu.Lock()
	defer configMu.Unlock()

	return saveConfigLocked(
		filename,
		cfg,
	)
}

// ============================================================
// saveConfigLocked
// ============================================================

func saveConfigLocked(
	filename string,
	cfg Config,
) error {
	if strings.TrimSpace(filename) == "" {
		filename = ConfigFile
	}

	data, err := json.MarshalIndent(
		cfg,
		"",
		"    ",
	)

	if err != nil {
		return fmt.Errorf(
			"生成配置 JSON 失败: %w",
			err,
		)
	}

	data = append(data, '\n')

	// --------------------------------------------------------
	// 确保目录存在
	// --------------------------------------------------------

	dir := filepath.Dir(filename)

	if dir != "." {
		if err := os.MkdirAll(
			dir,
			0755,
		); err != nil {
			return fmt.Errorf(
				"创建配置目录失败: %w",
				err,
			)
		}
	}

	// --------------------------------------------------------
	// 临时文件
	// --------------------------------------------------------

	tempFile := filename + ".tmp"

	if err := os.WriteFile(
		tempFile,
		data,
		0600,
	); err != nil {
		return fmt.Errorf(
			"写入临时配置失败: %w",
			err,
		)
	}

	// --------------------------------------------------------
	// 原子替换
	// --------------------------------------------------------

	if err := os.Rename(
		tempFile,
		filename,
	); err != nil {
		_ = os.Remove(tempFile)

		return fmt.Errorf(
			"替换配置文件失败: %w",
			err,
		)
	}

	return nil
}

// ============================================================
// mergeDefaultConfig
//
// 兼容旧版本 config.json。
// ============================================================

func mergeDefaultConfig(
	cfg *Config,
	def Config,
) {
	// --------------------------------------------------------
	// Qbot
	// --------------------------------------------------------

	if cfg.Name == "" {
		cfg.Name = def.Name
	}

	if cfg.Version == "" {
		cfg.Version = def.Version
	}

	// --------------------------------------------------------
	// QQ
	// --------------------------------------------------------

	if cfg.QQ.Provider == "" {
		cfg.QQ.Provider =
			def.QQ.Provider
	}

	if cfg.QQ.ReconnectInterval <= 0 {
		cfg.QQ.ReconnectInterval =
			def.QQ.ReconnectInterval
	}

	// --------------------------------------------------------
	// AI
	// --------------------------------------------------------

	if cfg.AI.BaseURL == "" {
		cfg.AI.BaseURL =
			def.AI.BaseURL
	}

	if cfg.AI.Model == "" {
		cfg.AI.Model =
			def.AI.Model
	}

	if cfg.AI.Temperature < 0 {
		cfg.AI.Temperature =
			def.AI.Temperature
	}

	if cfg.AI.MaxTokens <= 0 {
		cfg.AI.MaxTokens =
			def.AI.MaxTokens
	}

	if cfg.AI.TopP <= 0 {
		cfg.AI.TopP =
			def.AI.TopP
	}

	if cfg.AI.SystemPrompt == "" {
		cfg.AI.SystemPrompt =
			def.AI.SystemPrompt
	}

	if cfg.AI.MemoryLimit <= 0 {
		cfg.AI.MemoryLimit =
			def.AI.MemoryLimit
	}

	if cfg.AI.Timeout <= 0 {
		cfg.AI.Timeout =
			def.AI.Timeout
	}

	// --------------------------------------------------------
	// Group AI
	// --------------------------------------------------------

	if cfg.GroupAI.Probability < 0 ||
		cfg.GroupAI.Probability > 1 {

		cfg.GroupAI.Probability =
			def.GroupAI.Probability
	}

	if cfg.GroupAI.Cooldown <= 0 {
		cfg.GroupAI.Cooldown =
			def.GroupAI.Cooldown
	}

	if cfg.GroupAI.MaxRepliesPerHour <= 0 {
		cfg.GroupAI.MaxRepliesPerHour =
			def.GroupAI.MaxRepliesPerHour
	}

	// --------------------------------------------------------
	// Memory
	// --------------------------------------------------------

	if cfg.Memory.File == "" {
		cfg.Memory.File =
			def.Memory.File
	}

	if cfg.Memory.MaxMessages <= 0 {
		cfg.Memory.MaxMessages =
			def.Memory.MaxMessages
	}

	if cfg.Memory.SaveInterval <= 0 {
		cfg.Memory.SaveInterval =
			def.Memory.SaveInterval
	}

	// --------------------------------------------------------
	// Web
	// --------------------------------------------------------

	if cfg.Web.Host == "" {
		cfg.Web.Host =
			def.Web.Host
	}

	if cfg.Web.Port <= 0 {
		cfg.Web.Port =
			def.Web.Port
	}

	// --------------------------------------------------------
	// Game
	// --------------------------------------------------------

	if cfg.Game.Cooldown <= 0 {
		cfg.Game.Cooldown =
			def.Game.Cooldown
	}
}

// ============================================================
// GetConfig
// ============================================================

func GetConfig() Config {
	configMu.RLock()
	defer configMu.RUnlock()

	return QbotConfig
}

// ============================================================
// UpdateConfig
// ============================================================
//
// WebUI 修改配置时使用。
// ============================================================

func UpdateConfig(
	cfg Config,
) error {
	configMu.Lock()
	defer configMu.Unlock()

	QbotConfig = cfg

	return saveConfigLocked(
		ConfigFile,
		QbotConfig,
	)
}

// ============================================================
// SetConfig
// ============================================================
//
// UpdateConfig 的简化版本。
// ============================================================

func SetConfig(
	cfg Config,
) error {
	return UpdateConfig(cfg)
}

// ============================================================
// ResetConfig
// ============================================================
//
// WebUI 可以调用，用于恢复默认配置。
// ============================================================

func ResetConfig() error {
	configMu.Lock()
	defer configMu.Unlock()

	QbotConfig =
		DefaultConfig()

	return saveConfigLocked(
		ConfigFile,
		QbotConfig,
	)
}

// ============================================================
// GetConfigFile
// ============================================================

func GetConfigFile() string {
	return ConfigFile
}

// ============================================================
// EnsureQbotDirectories
// ============================================================
//
// 目前 Qbot 不强制建立复杂目录结构。
// 这里保留接口，方便以后扩展。
// ============================================================

func EnsureQbotDirectories() error {
	return nil
}

// ============================================================
// End of init.go
// ============================================================