package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ============================================================
// ai.go
//
// Qbot AI 核心
//
// 支持 OpenAI Compatible API：
//
//   OpenAI
//   OpenRouter
//   DeepSeek
//   SiliconFlow
//   Ollama
//   LM Studio
//   OneAPI
//   NewAPI
//   其他兼容 /v1/chat/completions 的服务
//
// 同时负责：
//
//   AI 状态
//   AI Chat
//   Streaming
//   Memory
//   Group AI
//
// 配置统一来自 init.go：
//
//   QbotConfig.AI
//   QbotConfig.GroupAI
//   QbotConfig.Memory
// ============================================================

// ============================================================
// AI Ready
// ============================================================

var aiReadyMu sync.RWMutex

var aiReady bool

func SetAIReady(ready bool) {
	aiReadyMu.Lock()
	aiReady = ready
	aiReadyMu.Unlock()
}

func IsAIReady() bool {
	aiReadyMu.RLock()
	defer aiReadyMu.RUnlock()

	return aiReady
}

// ============================================================
// AI Client
// ============================================================

type AIClient struct {
	mu sync.RWMutex

	httpClient *http.Client

	baseURL string

	apiKey string

	model string
}

// ============================================================
// NewAIClient
// ============================================================

func NewAIClient() *AIClient {
	cfg := GetConfig()

	timeout := cfg.AI.Timeout

	if timeout <= 0 {
		timeout = 60
	}

	return &AIClient{
		httpClient: &http.Client{
			Timeout: time.Duration(timeout) * time.Second,
		},

		baseURL: normalizeAIBaseURL(
			cfg.AI.BaseURL,
		),

		apiKey: cfg.AI.APIKey,

		model: cfg.AI.Model,
	}
}

// ============================================================
// Global AI Client
// ============================================================

var globalAIClient *AIClient

var globalAIClientMu sync.Mutex

// ============================================================
// GetAIClient
// ============================================================

func GetAIClient() *AIClient {
	globalAIClientMu.Lock()
	defer globalAIClientMu.Unlock()

	cfg := GetConfig()

	timeout := cfg.AI.Timeout

	if timeout <= 0 {
		timeout = 60
	}

	baseURL := normalizeAIBaseURL(
		cfg.AI.BaseURL,
	)

	if globalAIClient == nil {
		globalAIClient = &AIClient{
			httpClient: &http.Client{
				Timeout: time.Duration(timeout) * time.Second,
			},

			baseURL: baseURL,

			apiKey: cfg.AI.APIKey,

			model: cfg.AI.Model,
		}

		return globalAIClient
	}

	globalAIClient.httpClient.Timeout =
		time.Duration(timeout) * time.Second

	globalAIClient.baseURL =
		baseURL

	globalAIClient.apiKey =
		cfg.AI.APIKey

	globalAIClient.model =
		cfg.AI.Model

	return globalAIClient
}

// ============================================================
// normalizeAIBaseURL
// ============================================================

func normalizeAIBaseURL(
	baseURL string,
) string {
	baseURL = strings.TrimSpace(baseURL)

	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	baseURL =
		strings.TrimRight(
			baseURL,
			"/",
		)

	if !strings.HasSuffix(
		baseURL,
		"/v1",
	) {
		baseURL += "/v1"
	}

	return baseURL
}

// ============================================================
// OpenAI API Types
// ============================================================

type ChatMessage struct {
	Role string `json:"role"`

	Content string `json:"content"`
}

type ChatRequest struct {
	Model string `json:"model"`

	Messages []ChatMessage `json:"messages"`

	Temperature *float64 `json:"temperature,omitempty"`

	MaxTokens int `json:"max_tokens,omitempty"`

	TopP *float64 `json:"top_p,omitempty"`

	Stream bool `json:"stream"`
}

type ChatChoice struct {
	Index int `json:"index"`

	Message ChatMessage `json:"message"`

	FinishReason string `json:"finish_reason"`
}

type ChatUsage struct {
	PromptTokens int `json:"prompt_tokens"`

	CompletionTokens int `json:"completion_tokens"`

	TotalTokens int `json:"total_tokens"`
}

type ChatResponse struct {
	ID string `json:"id"`

	Object string `json:"object"`

	Created int64 `json:"created"`

	Model string `json:"model"`

	Choices []ChatChoice `json:"choices"`

	Usage ChatUsage `json:"usage"`
}

// ============================================================
// Chat
// ============================================================

func (c *AIClient) Chat(
	ctx context.Context,
	messages []ChatMessage,
) (string, error) {

	if c == nil {
		return "", errors.New(
			"AI client is nil",
		)
	}

	c.mu.RLock()

	baseURL := c.baseURL

	apiKey := c.apiKey

	model := c.model

	client := c.httpClient

	c.mu.RUnlock()

	if strings.TrimSpace(
		baseURL,
	) == "" {
		return "", errors.New(
			"AI BaseURL 未配置",
		)
	}

	if strings.TrimSpace(
		model,
	) == "" {
		return "", errors.New(
			"AI Model 未配置",
		)
	}

	cfg := GetConfig()

	request := ChatRequest{
		Model: model,

		Messages: messages,

		Temperature:
			float64Ptr(
				cfg.AI.Temperature,
			),

		MaxTokens:
			cfg.AI.MaxTokens,

		TopP:
			float64Ptr(
				cfg.AI.TopP,
			),

		Stream: false,
	}

	data, err := json.Marshal(
		request,
	)

	if err != nil {
		return "",
			fmt.Errorf(
				"编码 AI 请求失败: %w",
				err,
			)
	}

	endpoint :=
		strings.TrimRight(
			baseURL,
			"/",
		) +
			"/chat/completions"

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(data),
	)

	if err != nil {
		return "",
			fmt.Errorf(
				"创建 AI 请求失败: %w",
				err,
			)
	}

	req.Header.Set(
		"Content-Type",
		"application/json",
	)

	if apiKey != "" {
		req.Header.Set(
			"Authorization",
			"Bearer "+apiKey,
		)
	}

	resp, err :=
		client.Do(req)

	if err != nil {
		SetAIReady(false)

		return "",
			fmt.Errorf(
				"请求 AI 失败: %w",
				err,
			)
	}

	defer resp.Body.Close()

	body, err :=
		io.ReadAll(resp.Body)

	if err != nil {
		SetAIReady(false)

		return "",
			fmt.Errorf(
				"读取 AI 响应失败: %w",
				err,
			)
	}

	if resp.StatusCode < 200 ||
		resp.StatusCode >= 300 {

		SetAIReady(false)

		return "",
			fmt.Errorf(
				"AI HTTP %d: %s",
				resp.StatusCode,
				strings.TrimSpace(
					string(body),
				),
			)
	}

	var result ChatResponse

	if err := json.Unmarshal(
		body,
		&result,
	); err != nil {

		SetAIReady(false)

		return "",
			fmt.Errorf(
				"解析 AI 响应失败: %w",
				err,
			)
	}

	if len(result.Choices) == 0 {
		SetAIReady(false)

		return "",
			errors.New(
				"AI 返回结果为空",
			)
	}

	SetAIReady(true)

	return strings.TrimSpace(
		result.Choices[0].Message.Content,
	), nil
}

// ============================================================
// ChatSimple
// ============================================================

func (c *AIClient) ChatSimple(
	ctx context.Context,
	prompt string,
) (string, error) {

	cfg := GetConfig()

	messages :=
		[]ChatMessage{}

	if strings.TrimSpace(
		cfg.AI.SystemPrompt,
	) != "" {

		messages =
			append(
				messages,
				ChatMessage{
					Role: "system",

					Content:
						cfg.AI.SystemPrompt,
				},
			)
	}

	messages =
		append(
			messages,
			ChatMessage{
				Role: "user",

				Content: prompt,
			},
		)

	return c.Chat(
		ctx,
		messages,
	)
}

// ============================================================
// AIChat
//
// 统一给其他文件调用。
// ============================================================

func AIChat(
	ctx context.Context,
	prompt string,
) (string, error) {

	cfg := GetConfig()

	if !cfg.AI.Enabled {
		return "",
			errors.New(
				"AI 未启用",
			)
	}

	client :=
		GetAIClient()

	return client.ChatSimple(
		ctx,
		prompt,
	)
}

// ============================================================
// TestAI
// ============================================================

func TestAI(message string) (string, error) {
	message = strings.TrimSpace(message)

	if message == "" {
		message = "请回复：Qbot AI 连接正常。"
	}

	return AIChat(
		context.Background(),
		message,
	)
}

// ============================================================
// StreamChat
// ============================================================
//
// OpenAI Compatible SSE。
// ============================================================

func (c *AIClient) StreamChat(
	ctx context.Context,
	messages []ChatMessage,
	onToken func(string),
) error {

	if c == nil {
		return errors.New(
			"AI client is nil",
		)
	}

	c.mu.RLock()

	baseURL := c.baseURL

	apiKey := c.apiKey

	model := c.model

	client := c.httpClient

	c.mu.RUnlock()

	cfg := GetConfig()

	request := ChatRequest{
		Model: model,

		Messages: messages,

		Temperature:
			float64Ptr(
				cfg.AI.Temperature,
			),

		MaxTokens:
			cfg.AI.MaxTokens,

		TopP:
			float64Ptr(
				cfg.AI.TopP,
			),

		Stream: true,
	}

	data, err := json.Marshal(
		request,
	)

	if err != nil {
		return fmt.Errorf(
			"编码 Stream 请求失败: %w",
			err,
		)
	}

	endpoint :=
		strings.TrimRight(
			baseURL,
			"/",
		) +
			"/chat/completions"

	req, err :=
		http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			endpoint,
			bytes.NewReader(data),
		)

	if err != nil {
		return err
	}

	req.Header.Set(
		"Content-Type",
		"application/json",
	)

	req.Header.Set(
		"Accept",
		"text/event-stream",
	)

	if apiKey != "" {
		req.Header.Set(
			"Authorization",
			"Bearer "+apiKey,
		)
	}

	resp, err :=
		client.Do(req)

	if err != nil {
		SetAIReady(false)

		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode < 200 ||
		resp.StatusCode >= 300 {

		body, _ :=
			io.ReadAll(resp.Body)

		SetAIReady(false)

		return fmt.Errorf(
			"AI HTTP %d: %s",
			resp.StatusCode,
			strings.TrimSpace(
				string(body),
			),
		)
	}

	scanner :=
		bufio.NewScanner(
			resp.Body,
		)

	// 增大 Scanner 缓冲区。
	scanner.Buffer(
		make([]byte, 4096),
		1024*1024,
	)

	for scanner.Scan() {

		line :=
			strings.TrimSpace(
				scanner.Text(),
			)

		if line == "" {
			continue
		}

		if !strings.HasPrefix(
			line,
			"data:",
		) {
			continue
		}

		dataLine :=
			strings.TrimSpace(
				strings.TrimPrefix(
					line,
					"data:",
				),
			)

		if dataLine == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}

		if err := json.Unmarshal(
			[]byte(dataLine),
			&chunk,
		); err != nil {
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		token :=
			chunk.Choices[0].
				Delta.Content

		if token != "" &&
			onToken != nil {

			onToken(token)
		}
	}

	if err := scanner.Err();
		err != nil {

		SetAIReady(false)

		return fmt.Errorf(
			"读取 AI Stream 失败: %w",
			err,
		)
	}

	SetAIReady(true)

	return nil
}

// ============================================================
// AIWithMemory
// ============================================================

func AIWithMemory(
	ctx context.Context,
	userID string,
	groupID string,
	prompt string,
) (string, error) {

	cfg := GetConfig()

	if !cfg.AI.Enabled {
		return "",
			errors.New(
				"AI 未启用",
			)
	}

	messages :=
		[]ChatMessage{}

	// --------------------------------------------------------
	// System
	// --------------------------------------------------------

	if cfg.AI.SystemPrompt != "" {

		messages =
			append(
				messages,
				ChatMessage{
					Role: "system",

					Content:
						cfg.AI.SystemPrompt,
				},
			)
	}

	// --------------------------------------------------------
	// Memory
	// --------------------------------------------------------

	if cfg.AI.MemoryEnabled &&
		cfg.Memory.Enabled {

		limit :=
			cfg.AI.MemoryLimit

		if limit <= 0 {
			limit = 20
		}

		items :=
			GetMemory(
				userID,
				groupID,
				limit,
			)

		for _, item :=
			range items {

			role :=
				item.Role

			if role != "user" &&
				role != "assistant" &&
				role != "system" {

				role = "user"
			}

			messages =
				append(
					messages,
					ChatMessage{
						Role: role,

						Content:
							item.Content,
					},
				)
		}
	}

	// --------------------------------------------------------
	// 当前消息
	// --------------------------------------------------------

	messages =
		append(
			messages,
			ChatMessage{
				Role: "user",

				Content: prompt,
			},
		)

	client :=
		GetAIClient()

	reply, err :=
		client.Chat(
			ctx,
			messages,
		)

	if err != nil {
		return "",
			err
	}

	// --------------------------------------------------------
	// Memory
	// --------------------------------------------------------

	if cfg.Memory.Enabled {

		_ = AddMemory(
			userID,
			groupID,
			"user",
			prompt,
		)

		_ = AddMemory(
			userID,
			groupID,
			"assistant",
			reply,
		)
	}

	return reply, nil
}

// ============================================================
// Group AI
// ============================================================

func GroupAIEnabled() bool {

	cfg := GetConfig()

	return cfg.AI.Enabled &&
		cfg.GroupAI.Enabled
}

// ============================================================
// ShouldGroupAIReply
// ============================================================
//
// 判断群消息是否触发 AI。
// ============================================================

func ShouldGroupAIReply(
	message string,
	mentioned bool,
) bool {

	cfg := GetConfig()

	if !cfg.AI.Enabled {
		return false
	}

	if !cfg.GroupAI.Enabled {
		return false
	}

	if !cfg.GroupAI.AutoReply {
		return false
	}

	if cfg.GroupAI.RequireAt &&
		!mentioned {

		return false
	}

	// --------------------------------------------------------
	// 概率
	// --------------------------------------------------------

	probability :=
		cfg.GroupAI.Probability

	if probability <= 0 {
		return false
	}

	if probability >= 1 {
		return true
	}

	return randomFloat64() < probability
}

// ============================================================
// BuildGroupAIPrompt
// ============================================================

func BuildGroupAIPrompt(
	nickname string,
	message string,
) string {

	nickname =
		strings.TrimSpace(
			nickname,
		)

	message =
		strings.TrimSpace(
			message,
		)

	if nickname == "" {
		nickname = "用户"
	}

	return fmt.Sprintf(
		"%s在QQ群中说：\n%s\n\n请直接自然地回复这条消息，不要描述自己的思考过程。",
		nickname,
		message,
	)
}

// ============================================================
// GroupAIReply
// ============================================================

func GroupAIReply(
	ctx context.Context,
	userID string,
	groupID string,
	nickname string,
	message string,
) (string, error) {

	prompt :=
		BuildGroupAIPrompt(
			nickname,
			message,
		)

	return AIWithMemory(
		ctx,
		userID,
		groupID,
		prompt,
	)
}

// ============================================================
// Memory
// ============================================================

type MemoryItem struct {
	UserID string `json:"user_id"`

	GroupID string `json:"group_id"`

	Role string `json:"role"`

	Content string `json:"content"`

	Time string `json:"time"`
}

var memoryMu sync.RWMutex

var memoryData []MemoryItem

// ============================================================
// InitMemory
// ============================================================

func InitMemory() error {

	memoryMu.Lock()
	defer memoryMu.Unlock()

	cfg :=
		GetConfig()

	if !cfg.Memory.Enabled {
		memoryData =
			[]MemoryItem{}

		return nil
	}

	filename :=
		cfg.Memory.File

	if strings.TrimSpace(
		filename,
	) == "" {

		filename =
			MemoryDefaultFile
	}

	data, err :=
		os.ReadFile(filename)

	if err != nil {

		if errors.Is(
			err,
			os.ErrNotExist,
		) {

			memoryData =
				[]MemoryItem{}

			return saveMemoryLocked()
		}

		return err
	}

	if len(data) == 0 {

		memoryData =
			[]MemoryItem{}

		return nil
	}

	var items []MemoryItem

	if err := json.Unmarshal(
		data,
		&items,
	); err != nil {

		return fmt.Errorf(
			"解析 Memory 失败: %w",
			err,
		)
	}

	memoryData =
		items

	limit :=
		cfg.Memory.MaxMessages

	if limit > 0 &&
		len(memoryData) > limit {

		memoryData =
			memoryData[
				len(memoryData)-limit:
			]
	}

	return nil
}

// ============================================================
// AddMemory
// ============================================================

func AddMemory(
	userID string,
	groupID string,
	role string,
	content string,
) error {

	content =
		strings.TrimSpace(
			content,
		)

	if content == "" {
		return nil
	}

	memoryMu.Lock()
	defer memoryMu.Unlock()

	memoryData =
		append(
			memoryData,
			MemoryItem{
				UserID: userID,

				GroupID: groupID,

				Role: role,

				Content: content,

				Time:
					time.Now().
						Format(
							"2006-01-02 15:04:05",
						),
			},
		)

	cfg :=
		GetConfig()

	limit :=
		cfg.Memory.MaxMessages

	if limit > 0 &&
		len(memoryData) > limit {

		removeCount :=
			len(memoryData) - limit

		memoryData =
			memoryData[
				removeCount:
			]
	}

	return saveMemoryLocked()
}

// ============================================================
// GetMemory
// ============================================================

func GetMemory(
	userID string,
	groupID string,
	limit int,
) []MemoryItem {

	memoryMu.RLock()
	defer memoryMu.RUnlock()

	result :=
		[]MemoryItem{}

	for i :=
		len(memoryData) - 1;
		i >= 0;
		i-- {

		item :=
			memoryData[i]

		if userID != "" &&
			item.UserID != userID {

			continue
		}

		if groupID != "" &&
			item.GroupID != groupID {

			continue
		}

		result =
			append(
				result,
				item,
			)

		if limit > 0 &&
			len(result) >= limit {

			break
		}
	}

	// --------------------------------------------------------
	// 反转成正序
	// --------------------------------------------------------

	for i, j := 0, len(result)-1;
		i < j;
		i, j = i+1, j-1 {

		result[i],
			result[j] =
			result[j],
				result[i]
	}

	return result
}

// ============================================================
// ReadMemory
// ============================================================

func ReadMemory() (
	string,
	error,
) {

	memoryMu.RLock()
	defer memoryMu.RUnlock()

	data, err :=
		json.MarshalIndent(
			memoryData,
			"",
			"    ",
		)

	if err != nil {
		return "",
			err
	}

	return string(data),
		nil
}

// ============================================================
// MemoryCount
// ============================================================

func MemoryCount() int {

	memoryMu.RLock()
	defer memoryMu.RUnlock()

	return len(memoryData)
}

// ============================================================
// ClearMemory
// ============================================================

func ClearMemory() error {

	memoryMu.Lock()
	defer memoryMu.Unlock()

	memoryData =
		[]MemoryItem{}

	return saveMemoryLocked()
}

// ============================================================
// saveMemoryLocked
// ============================================================

func saveMemoryLocked() error {

	cfg :=
		GetConfig()

	filename :=
		cfg.Memory.File

	if strings.TrimSpace(
		filename,
	) == "" {

		filename =
			MemoryDefaultFile
	}

	data, err :=
		json.MarshalIndent(
			memoryData,
			"",
			"    ",
		)

	if err != nil {
		return err
	}

	data =
		append(
			data,
			'\n',
		)

	tempFile :=
		filename + ".tmp"

	if err := os.WriteFile(
		tempFile,
		data,
		0600,
	); err != nil {

		return err
	}

	if err := os.Rename(
		tempFile,
		filename,
	); err != nil {

		_ = os.Remove(
			tempFile,
		)

		return err
	}

	return nil
}

// ============================================================
// Utility
// ============================================================

func float64Ptr(
	value float64,
) *float64 {
	return &value
}

// ============================================================
// randomFloat64
// ============================================================
//
// 使用时间生成轻量随机值。
// 群自动回复只需要概率判断，不需要密码学随机。
// ============================================================

func randomFloat64() float64 {

	now :=
		time.Now().UnixNano()

	x :=
		uint64(now)

	x ^= x << 13

	x ^= x >> 7

	x ^= x << 17

	return float64(
		x%1000000,
	) / 1000000.0
}

// ============================================================
// AIConfigValid
// ============================================================

func AIConfigValid() error {

	cfg :=
		GetConfig()

	if !cfg.AI.Enabled {
		return errors.New(
			"AI 未启用",
		)
	}

	if strings.TrimSpace(
		cfg.AI.BaseURL,
	) == "" {

		return errors.New(
			"Base URL 为空",
		)
	}

	if strings.TrimSpace(
		cfg.AI.Model,
	) == "" {

		return errors.New(
			"Model 为空",
		)
	}

	return nil
}

// ============================================================
// ReloadAI
// ============================================================
//
// WebUI 修改 AI 设置之后调用。
// ============================================================

func ReloadAI() error {
	globalAIClientMu.Lock()

	globalAIClient = nil

	globalAIClientMu.Unlock()

	SetAIReady(false)

	return nil
}

// ============================================================
// EnableAI
// ============================================================

func EnableAI(
	enabled bool,
) error {

	configMu.Lock()

	QbotConfig.AI.Enabled =
		enabled

	configMu.Unlock()

	if err := SaveConfig(
		ConfigFile,
		GetConfig(),
	); err != nil {

		return err
	}

	if !enabled {
		SetAIReady(false)
	}

	return nil
}

// ============================================================
// SetAIConfig
// ============================================================
//
// WebUI 可以用这个函数更新 AI 配置。
// ============================================================

func SetAIConfig(
	baseURL string,
	apiKey string,
	model string,
) error {

	configMu.Lock()

	QbotConfig.AI.BaseURL =
		strings.TrimSpace(
			baseURL,
		)

	QbotConfig.AI.APIKey =
		strings.TrimSpace(
			apiKey,
		)

	QbotConfig.AI.Model =
		strings.TrimSpace(
			model,
		)

	configMu.Unlock()

	ReloadAI()

	return SaveConfig(
		ConfigFile,
		GetConfig(),
	)
}

type AIStatus struct {
	Enabled bool   `json:"enabled"`
	Ready   bool   `json:"ready"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
}

func GetAIStatus() AIStatus {
	cfg := GetConfig()

	return AIStatus{
		Enabled: cfg.AI.Enabled,
		Ready:   IsAIReady(),
		BaseURL: cfg.AI.BaseURL,
		Model:   cfg.AI.Model,
	}
}

func AskAI(
	userID string,
	groupID string,
	message string,
) (string, error) {

	message = strings.TrimSpace(message)

	if message == "" {
		return "",
			errors.New("消息不能为空")
	}

	cfg := GetConfig()

	if !cfg.AI.Enabled {
		return "",
			errors.New("AI 未启用")
	}

	if cfg.AI.MemoryEnabled &&
		cfg.Memory.Enabled {

		return AIWithMemory(
			context.Background(),
			userID,
			groupID,
			message,
		)
	}

	return AIChat(
		context.Background(),
		message,
	)
}

// ============================================================
// End of ai.go
// ============================================================