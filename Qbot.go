package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const (
	Version = "v0.2.1"

	APIBaseURL = "https://api.bot.qq.com"
	TokenURL   = "https://bots.qq.com/app/getAppAccessToken"

	WebHost = "127.0.0.1"
	WebPort = 8080

	ReconnectMin = 3 * time.Second
	ReconnectMax = 30 * time.Second

	ConfigFile = "qbot_config.json"

	// Open-Meteo
	WeatherGeocodeURL = "https://geocoding-api.open-meteo.com/v1/search"
	WeatherAPIURL     = "https://api.open-meteo.com/v1/forecast"
)

//
// ============================================================
// 全局状态
// ============================================================
//

var (
	appID     string
	appSecret string

	accessToken string
	tokenExpire time.Time

	wsConn *websocket.Conn
	connMu sync.Mutex

	running bool
	ready   bool

	stateMu sync.RWMutex

	sessionID string
	botID     string
	botName   string

	sequence int64

	heartbeatInterval time.Duration

	lastHeartbeat    time.Time
	lastHeartbeatACK time.Time
	lastConnect      time.Time
	lastDisconnect   time.Time

	reconnectCount int

	stopBotChan chan struct{}

	logMu    sync.Mutex
	logLines []string

	configMu sync.RWMutex
	config   Config
)

//
// ============================================================
// 配置结构
// ============================================================
//

type Config struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`

	Weather WeatherConfig   `json:"weather"`
	Replies []CustomReply   `json:"replies"`
}

type WeatherConfig struct {
	Enabled      bool   `json:"enabled"`
	DefaultCity  string `json:"default_city"`
	Language     string `json:"language"`
}

type CustomReply struct {
	ID      int64  `json:"id"`
	Trigger string `json:"trigger"`
	Reply   string `json:"reply"`
	Mode    string `json:"mode"`
	Enabled bool   `json:"enabled"`
}

//
// ============================================================
// 日志
// ============================================================
//

func addLog(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	line := time.Now().Format("2006-01-02 15:04:05") + " " + text

	log.Println(text)

	logMu.Lock()
	logLines = append(logLines, line)

	if len(logLines) > 500 {
		logLines = logLines[len(logLines)-500:]
	}
	logMu.Unlock()
}

func getLogs() []string {
	logMu.Lock()
	defer logMu.Unlock()

	result := make([]string, len(logLines))
	copy(result, logLines)

	return result
}

//
// ============================================================
// 配置加载
// ============================================================
//

func defaultConfig() Config {
	return Config{
		Weather: WeatherConfig{
			Enabled:     true,
			DefaultCity: "东京",
			Language:    "zh",
		},
		Replies: []CustomReply{},
	}
}

func loadPersistentConfig() error {
	configMu.Lock()
	defer configMu.Unlock()

	cfg := defaultConfig()

	data, err := os.ReadFile(ConfigFile)

	if err != nil {
		if os.IsNotExist(err) {
			config = cfg

			appID = ""
			appSecret = ""

			addLog("[CONFIG] 尚未配置 AppID / AppSecret")

			return saveConfigLocked()
		}

		return err
	}

	if len(data) == 0 {
		config = cfg
		return saveConfigLocked()
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("配置文件 JSON 错误: %w", err)
	}

	config = cfg

	appID = strings.TrimSpace(cfg.AppID)
	appSecret = strings.TrimSpace(cfg.AppSecret)

	addLog(
		"[CONFIG] 配置加载成功 AppID=%s",
		maskAppID(appID),
	)

	return nil
}

func saveConfigLocked() error {
	config.AppID = appID
	config.AppSecret = appSecret

	data, err := json.MarshalIndent(
		config,
		"",
		"  ",
	)

	if err != nil {
		return err
	}

	return os.WriteFile(
		ConfigFile,
		data,
		0600,
	)
}

func saveConfig() error {
	configMu.Lock()
	defer configMu.Unlock()

	return saveConfigLocked()
}

func maskAppID(id string) string {
	if id == "" {
		return "-"
	}

	if len(id) <= 6 {
		return "***"
	}

	return id[:3] + "***" + id[len(id)-3:]
}

//
// ============================================================
// QQ Token
// ============================================================
//

type TokenResponse struct {
	AccessToken string          `json:"access_token"`
	ExpiresIn   json.RawMessage `json:"expires_in"`
}

func parseExpiresIn(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 7200
	}

	var number int64

	if json.Unmarshal(raw, &number) == nil {
		if number > 0 {
			return number
		}
	}

	var text string

	if json.Unmarshal(raw, &text) == nil {
		if n, err := strconv.ParseInt(
			strings.TrimSpace(text),
			10,
			64,
		); err == nil && n > 0 {
			return n
		}
	}

	return 7200
}

func getAccessToken() error {
	configMu.RLock()
	id := strings.TrimSpace(appID)
	secret := strings.TrimSpace(appSecret)
	configMu.RUnlock()

	if id == "" || secret == "" {
		return fmt.Errorf("AppID 或 AppSecret 未配置")
	}

	addLog("[QQ] 正在获取 Access Token")

	payload := map[string]string{
		"appId":        id,
		"clientSecret": secret,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(
		http.MethodPost,
		TokenURL,
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
		"User-Agent",
		"Qbot/"+Version,
	)

	client := &http.Client{
		Timeout: 20 * time.Second,
	}

	resp, err := client.Do(req)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)

	if err != nil {
		return err
	}

	if resp.StatusCode < 200 ||
		resp.StatusCode >= 300 {

		return fmt.Errorf(
			"Token HTTP %d: %s",
			resp.StatusCode,
			string(body),
		)
	}

	var result TokenResponse

	if err := json.Unmarshal(
		body,
		&result,
	); err != nil {

		return fmt.Errorf(
			"Token JSON 解析失败: %w",
			err,
		)
	}

	if result.AccessToken == "" {
		return fmt.Errorf(
			"服务器没有返回 access_token: %s",
			string(body),
		)
	}

	expires := parseExpiresIn(
		result.ExpiresIn,
	)

	accessToken = result.AccessToken

	tokenExpire = time.Now().Add(
		time.Duration(expires) * time.Second,
	)

	addLog(
		"[QQ] Access Token 获取成功，有效期 %d 秒",
		expires,
	)

	return nil
}

//
// ============================================================
// Gateway
// ============================================================
//

type GatewayResponse struct {
	URL string `json:"url"`
}

func getGatewayURL() (string, error) {
	if accessToken == "" {
		return "", fmt.Errorf("Access Token 为空")
	}

	req, err := http.NewRequest(
		http.MethodGet,
		APIBaseURL+"/gateway",
		nil,
	)

	if err != nil {
		return "", err
	}

	req.Header.Set(
		"Authorization",
		"QQBot "+accessToken,
	)

	req.Header.Set(
		"User-Agent",
		"Qbot/"+Version,
	)

	client := &http.Client{
		Timeout: 20 * time.Second,
	}

	resp, err := client.Do(req)

	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf(
			"Gateway HTTP %d: %s",
			resp.StatusCode,
			string(body),
		)
	}

	var result GatewayResponse

	if err := json.Unmarshal(
		body,
		&result,
	); err != nil {
		return "", err
	}

	if result.URL == "" {
		return "", fmt.Errorf(
			"Gateway URL 为空: %s",
			string(body),
		)
	}

	return result.URL, nil
}

//
// ============================================================
// Gateway 数据结构
// ============================================================
//

type GatewayPacket struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s"`
	T  string          `json:"t"`
}

type HelloData struct {
	HeartbeatInterval int64 `json:"heartbeat_interval"`
}

type IdentifyData struct {
	Token      string            `json:"token"`
	Intents    int               `json:"intents"`
	Shard      []int             `json:"shard"`
	Properties map[string]string `json:"properties,omitempty"`
}

type ReadyData struct {
	Version   int       `json:"version"`
	SessionID string    `json:"session_id"`
	User      ReadyUser `json:"user"`
}

type ReadyUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot"`
}

//
// ============================================================
// Intent
// ============================================================
//

const (
	IntentGuilds               = 1 << 0
	IntentGuildMembers         = 1 << 1
	IntentGuildMessages        = 1 << 9
	IntentGuildMessageReaction = 1 << 10
	IntentDirectMessage        = 1 << 12
	IntentC2CAndGroup          = 1 << 25
)

//
// ============================================================
// Bot 启动停止
// ============================================================
//

func startGateway() {
	stateMu.Lock()

	if running {
		stateMu.Unlock()

		addLog("[BOT] 已经在运行")

		return
	}

	running = true
	ready = false
	reconnectCount = 0

	stopBotChan = make(chan struct{})

	stateMu.Unlock()

	go gatewayLoop()

	addLog("[BOT] 启动成功")
}

func stopGateway() {
	stateMu.Lock()

	if !running {
		stateMu.Unlock()

		addLog("[BOT] 当前没有运行")

		return
	}

	running = false
	ready = false

	ch := stopBotChan
	stopBotChan = nil

	stateMu.Unlock()

	if ch != nil {
		close(ch)
	}

	closeWebSocket()

	addLog("[BOT] 已停止")
}

func isRunning() bool {
	stateMu.RLock()
	defer stateMu.RUnlock()

	return running
}

func stopBotChannel() <-chan struct{} {
	stateMu.RLock()
	ch := stopBotChan
	stateMu.RUnlock()

	if ch == nil {
		ch = make(chan struct{})
	}

	return ch
}

//
// ============================================================
// Gateway 主循环
// ============================================================
//

func gatewayLoop() {
	delay := ReconnectMin

	for {
		if !isRunning() {
			return
		}

		if accessToken == "" ||
			time.Now().After(
				tokenExpire.Add(-2*time.Minute),
			) {

			err := getAccessToken()

			if err != nil {
				addLog(
					"[ERROR] 获取 Access Token 失败: %v",
					err,
				)

				sleepOrStop(delay)

				delay *= 2

				if delay > ReconnectMax {
					delay = ReconnectMax
				}

				continue
			}
		}

		gatewayURL, err := getGatewayURL()

		if err != nil {
			addLog(
				"[ERROR] 获取 Gateway 失败: %v",
				err,
			)

			sleepOrStop(delay)

			delay *= 2

			if delay > ReconnectMax {
				delay = ReconnectMax
			}

			continue
		}

		addLog(
			"[GATEWAY] Gateway: %s",
			gatewayURL,
		)

		err = connectGateway(gatewayURL)

		if err != nil {
			addLog(
				"[GATEWAY] 连接结束: %v",
				err,
			)
		}

		if !isRunning() {
			return
		}

		stateMu.Lock()

		reconnectCount++
		lastDisconnect = time.Now()

		stateMu.Unlock()

		addLog(
			"[GATEWAY] %s 后重新连接",
			delay,
		)

		sleepOrStop(delay)

		delay *= 2

		if delay > ReconnectMax {
			delay = ReconnectMax
		}
	}
}

//
// ============================================================
// Gateway 连接
// ============================================================
//

func connectGateway(gatewayURL string) error {
	addLog("[GATEWAY] 正在连接 WebSocket")

	dialer := websocket.DefaultDialer

	conn, _, err := dialer.Dial(
		gatewayURL,
		nil,
	)

	if err != nil {
		return err
	}

	connMu.Lock()
	wsConn = conn
	connMu.Unlock()

	stateMu.Lock()

	lastConnect = time.Now()
	ready = false

	stateMu.Unlock()

	addLog("[GATEWAY] WebSocket 已连接")

	defer func() {
		connMu.Lock()

		if wsConn == conn {
			_ = conn.Close()
			wsConn = nil
		}

		connMu.Unlock()
	}()

	for {
		if !isRunning() {
			return nil
		}

		_, data, err := conn.ReadMessage()

		if err != nil {
			return err
		}

		var packet GatewayPacket

		if err := json.Unmarshal(
			data,
			&packet,
		); err != nil {

			addLog(
				"[ERROR] Gateway JSON 解析失败: %v",
				err,
			)

			continue
		}

		if packet.S != nil {
			stateMu.Lock()

			sequence = *packet.S

			stateMu.Unlock()
		}

		if err := handlePacket(
			packet,
			conn,
		); err != nil {
			return err
		}
	}
}

func closeWebSocket() {
	connMu.Lock()
	defer connMu.Unlock()

	if wsConn != nil {
		_ = wsConn.Close()
		wsConn = nil
	}
}

//
// ============================================================
// Gateway Packet
// ============================================================
//

func handlePacket(
	packet GatewayPacket,
	conn *websocket.Conn,
) error {

	switch packet.Op {

	case 0:
		return handleDispatch(packet)

	case 1:
		addLog(
			"[GATEWAY] 收到服务器 Heartbeat 请求",
		)

		return sendHeartbeat()

	case 7:
		addLog(
			"[GATEWAY] 服务器要求重新连接",
		)

		return fmt.Errorf(
			"server requested reconnect",
		)

	case 9:
		addLog(
			"[GATEWAY] Invalid Session",
		)

		return fmt.Errorf(
			"invalid session",
		)

	case 10:
		return handleHello(
			packet,
			conn,
		)

	case 11:
		stateMu.Lock()

		lastHeartbeatACK = time.Now()

		stateMu.Unlock()

		addLog(
			"[GATEWAY] Heartbeat ACK",
		)

	default:
		addLog(
			"[GATEWAY] 未处理 Opcode=%d",
			packet.Op,
		)
	}

	return nil
}

//
// ============================================================
// Hello
// ============================================================
//

func handleHello(
	packet GatewayPacket,
	conn *websocket.Conn,
) error {

	var hello HelloData

	if err := json.Unmarshal(
		packet.D,
		&hello,
	); err != nil {
		return err
	}

	interval := time.Duration(
		hello.HeartbeatInterval,
	) * time.Millisecond

	if interval <= 0 {
		interval = 30 * time.Second
	}

	stateMu.Lock()
	heartbeatInterval = interval
	stateMu.Unlock()

	addLog(
		"[GATEWAY] Hello，心跳间隔 %s",
		interval,
	)

	if err := sendIdentify(); err != nil {
		return err
	}

	/*
		每一个 WebSocket 连接只启动一个
		对应的 heartbeat goroutine。

		避免重连后旧 heartbeat goroutine
		继续向新连接发送心跳。
	*/

	go heartbeatLoop(
		interval,
		conn,
	)

	return nil
}

//
// ============================================================
// Identify
// ============================================================
//

func sendIdentify() error {
    intents := IntentC2CAndGroup

    data := IdentifyData{
		Token: "QQBot " + accessToken,

		Intents: intents,

		Shard: []int{
			0,
			1,
		},

		Properties: map[string]string{
			"$os":      "windows",
			"$browser": "Qbot",
			"$device":  "Qbot",
		},
	}

    addLog(
        "[GATEWAY] Identify intents=%d",
        intents,
    )

    err := sendPacket(2, data)
    if err != nil {
        return err
    }

    addLog("[GATEWAY] Identify 已发送")

    return nil
}

//
// ============================================================
// 心跳
// ============================================================
//

func heartbeatLoop(
	interval time.Duration,
	conn *websocket.Conn,
) {

	ticker := time.NewTicker(interval)

	defer ticker.Stop()

	for {
		select {

		case <-ticker.C:

			if !isRunning() {
				return
			}

			connMu.Lock()

			sameConnection := wsConn == conn

			connMu.Unlock()

			if !sameConnection {
				return
			}

			if err := sendHeartbeat(); err != nil {

				addLog(
					"[HEARTBEAT] 发送失败: %v",
					err,
				)

				_ = conn.Close()

				return
			}

		case <-stopBotChannel():
			return
		}
	}
}

func sendHeartbeat() error {
	stateMu.RLock()

	seq := sequence

	stateMu.RUnlock()

	err := sendPacket(
		1,
		seq,
	)

	if err == nil {

		stateMu.Lock()

		lastHeartbeat = time.Now()

		stateMu.Unlock()

		addLog(
			"[HEARTBEAT] seq=%d",
			seq,
		)
	}

	return err
}

//
// ============================================================
// WebSocket 写
// ============================================================
//

func sendPacket(
	op int,
	data interface{},
) error {

	packet := map[string]interface{}{
		"op": op,
		"d":  data,
	}

	raw, err := json.Marshal(packet)

	if err != nil {
		return err
	}

	connMu.Lock()
	defer connMu.Unlock()

	if wsConn == nil {
		return fmt.Errorf(
			"WebSocket 未连接",
		)
	}

	return wsConn.WriteMessage(
		websocket.TextMessage,
		raw,
	)
}

//
// ============================================================
// Dispatch
// ============================================================
//

func handleDispatch(
	packet GatewayPacket,
) error {

	switch packet.T {

	case "READY":
		return handleReady(packet)

	case "RESUMED":

		addLog(
			"[GATEWAY] Session RESUMED",
		)

		stateMu.Lock()
		ready = true
		stateMu.Unlock()

		return nil

	case "C2C_MESSAGE_CREATE":
		return handleC2CMessage(packet)

	case "GROUP_AT_MESSAGE_CREATE":
		return handleGroupMessage(packet)

	case "GROUP_MESSAGE_CREATE":
		return handleGroupMessage(packet)

	case "AT_MESSAGE_CREATE":
		return handleChannelMessage(packet)

	case "DIRECT_MESSAGE_CREATE":
		return handleDirectMessage(packet)

	default:

		addLog(
			"[EVENT] %s",
			packet.T,
		)

		return nil
	}
}

//
// ============================================================
// READY
// ============================================================
//

func handleReady(
	packet GatewayPacket,
) error {

	var readyData ReadyData

	if err := json.Unmarshal(
		packet.D,
		&readyData,
	); err != nil {
		return err
	}

	stateMu.Lock()

	sessionID = readyData.SessionID
	botID = readyData.User.ID
	botName = readyData.User.Username
	ready = true
	reconnectCount = 0

	stateMu.Unlock()

	addLog(
		"======================================",
	)

	addLog(
		"[SUCCESS] QQ Bot 已连接",
	)

	addLog(
		"[BOT] %s (%s)",
		readyData.User.Username,
		readyData.User.ID,
	)

	addLog(
		"[SESSION] %s",
		readyData.SessionID,
	)

	addLog(
		"======================================",
	)

	return nil
}

//
// ============================================================
// QQ Message
// ============================================================
//

type MessageAuthor struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	Bot        bool   `json:"bot"`
	UserOpenID string `json:"user_openid"`
}

type QQMessage struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`

	Author MessageAuthor `json:"author"`

	UserOpenID  string `json:"user_openid"`
	GroupOpenID string `json:"group_openid"`

	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
}

//
// ============================================================
// C2C
// ============================================================
//

func handleC2CMessage(
	packet GatewayPacket,
) error {

	var msg QQMessage

	if err := json.Unmarshal(
		packet.D,
		&msg,
	); err != nil {
		return err
	}

	if msg.Author.Bot {
		return nil
	}

	userOpenID := msg.UserOpenID

	if userOpenID == "" {
		userOpenID = msg.Author.UserOpenID
	}

	content := strings.TrimSpace(
		msg.Content,
	)

	addLog(
		"[C2C] %s: %s",
		msg.Author.Username,
		content,
	)

	reply := makeReply(content)

	if reply == "" {
		return nil
	}

	return sendC2CMessage(
		userOpenID,
		reply,
		msg.ID,
	)
}

//
// ============================================================
// 群
// ============================================================
//

func handleGroupMessage(
	packet GatewayPacket,
) error {

	var msg QQMessage

	if err := json.Unmarshal(
		packet.D,
		&msg,
	); err != nil {
		return err
	}

	if msg.Author.Bot {
		return nil
	}

	content := strings.TrimSpace(
		msg.Content,
	)

	addLog(
		"[GROUP] %s: %s",
		msg.Author.Username,
		content,
	)

	reply := makeReply(content)

	if reply == "" {
		return nil
	}

	return sendGroupMessage(
		msg.GroupOpenID,
		reply,
		msg.ID,
	)
}

//
// ============================================================
// 频道
// ============================================================
//

func handleChannelMessage(
	packet GatewayPacket,
) error {

	var msg QQMessage

	if err := json.Unmarshal(
		packet.D,
		&msg,
	); err != nil {
		return err
	}

	if msg.Author.Bot {
		return nil
	}

	content := strings.TrimSpace(
		msg.Content,
	)

	addLog(
		"[CHANNEL] %s: %s",
		msg.Author.Username,
		content,
	)

	reply := makeReply(content)

	if reply == "" {
		return nil
	}

	return sendChannelMessage(
		msg.ChannelID,
		reply,
		msg.ID,
	)
}

//
// ============================================================
// DIRECT
// ============================================================
//

func handleDirectMessage(
	packet GatewayPacket,
) error {

	var msg QQMessage

	if err := json.Unmarshal(
		packet.D,
		&msg,
	); err != nil {
		return err
	}

	if msg.Author.Bot {
		return nil
	}

	content := strings.TrimSpace(
		msg.Content,
	)

	addLog(
		"[DIRECT] %s: %s",
		msg.Author.Username,
		content,
	)

	reply := makeReply(content)

	if reply == "" {
		return nil
	}

	return sendChannelMessage(
		msg.ChannelID,
		reply,
		msg.ID,
	)
}

//
// ============================================================
// 回复系统
// ============================================================
//

func makeReply(content string) string {
	content = strings.TrimSpace(content)

	lower := strings.ToLower(content)

	//
	// 系统指令
	//

	switch lower {

	case "你好":
		return "你好，我是 Qbot v0.2.1"

	case "hello":
		return "Hello!"

	case "hi":
		return "Hi!"

	case "ping":
		return "pong"

	case "/ping":
		return "pong"

	case "版本":
		return "Qbot v0.2.1"

	case "/version":
		return "Qbot v0.2.1"

	case "状态":
		return getBotStatusText()

	case "/status":
		return getBotStatusText()

	case "帮助":
		return "可用指令：你好、ping、版本、状态、天气、帮助"

	case "/help":
		return "可用指令：你好、ping、版本、状态、天气、帮助"
	}

	//
	// 天气
	//

	if lower == "天气" ||
		strings.HasPrefix(content, "天气 ") ||
		strings.HasPrefix(content, "天气　") {

		city := strings.TrimSpace(
			strings.TrimPrefix(
				strings.TrimPrefix(
					content,
					"天气",
				),
				"　",
			),
		)

		if city == "" {
			configMu.RLock()
			city = config.Weather.DefaultCity
			configMu.RUnlock()
		}

		return getWeatherReply(city)
	}

	//
	// 自定义回复
	//

	if reply := findCustomReply(content); reply != "" {
		return reply
	}

	return ""
}

func findCustomReply(content string) string {
	configMu.RLock()
	replies := append(
		[]CustomReply(nil),
		config.Replies...,
	)
	configMu.RUnlock()

	//
	// 先完全匹配
	//

	for _, item := range replies {

		if !item.Enabled {
			continue
		}

		trigger := strings.TrimSpace(
			item.Trigger,
		)

		if trigger == "" {
			continue
		}

		if item.Mode == "contains" {
			continue
		}

		if strings.EqualFold(
			content,
			trigger,
		) {
			addLog(
				"[REPLY] 完全匹配: %s",
				trigger,
			)

			return item.Reply
		}
	}

	//
	// 再包含匹配
	//

	for _, item := range replies {

		if !item.Enabled {
			continue
		}

		if item.Mode != "contains" {
			continue
		}

		trigger := strings.TrimSpace(
			item.Trigger,
		)

		if trigger == "" {
			continue
		}

		if strings.Contains(
			content,
			trigger,
		) {

			addLog(
				"[REPLY] 包含匹配: %s",
				trigger,
			)

			return item.Reply
		}
	}

	return ""
}

//
// ============================================================
// 状态
// ============================================================
//

func getBotStatusText() string {
	stateMu.RLock()
	defer stateMu.RUnlock()

	if ready {
		return "Qbot 当前在线，WebSocket 已连接。"
	}

	if running {
		return "Qbot 正在连接 QQ Gateway。"
	}

	return "Qbot 当前已停止。"
}

//
// ============================================================
// QQ REST API
// ============================================================
//

type SendMessageRequest struct {
	Content string `json:"content"`
	MsgType int    `json:"msg_type"`
	MsgID   string `json:"msg_id,omitempty"`
}

type APIError struct {
	ErrCode int    `json:"err_code"`
	Message string `json:"message"`
	TraceID string `json:"trace_id"`
}

func sendC2CMessage(
	userOpenID string,
	content string,
	msgID string,
) error {

	if userOpenID == "" {
		return fmt.Errorf(
			"user_openid 为空",
		)
	}

	apiURL :=
		APIBaseURL +
			"/v2/users/" +
			url.PathEscape(userOpenID) +
			"/messages"

	body := SendMessageRequest{
		Content: content,
		MsgType: 0,
		MsgID:   msgID,
	}

	return postAPI(
		apiURL,
		body,
	)
}

func sendGroupMessage(
	groupOpenID string,
	content string,
	msgID string,
) error {

	if groupOpenID == "" {
		return fmt.Errorf(
			"group_openid 为空",
		)
	}

	apiURL :=
		APIBaseURL +
			"/v2/groups/" +
			url.PathEscape(groupOpenID) +
			"/messages"

	body := SendMessageRequest{
		Content: content,
		MsgType: 0,
		MsgID:   msgID,
	}

	return postAPI(
		apiURL,
		body,
	)
}

func sendChannelMessage(
	channelID string,
	content string,
	msgID string,
) error {

	if channelID == "" {
		return fmt.Errorf(
			"channel_id 为空",
		)
	}

	apiURL :=
		APIBaseURL +
			"/v2/channels/" +
			url.PathEscape(channelID) +
			"/messages"

	body := SendMessageRequest{
		Content: content,
		MsgType: 0,
		MsgID:   msgID,
	}

	return postAPI(
		apiURL,
		body,
	)
}

func postAPI(
	apiURL string,
	body interface{},
) error {

	data, err := json.Marshal(body)

	if err != nil {
		return err
	}

	req, err := http.NewRequest(
		http.MethodPost,
		apiURL,
		bytes.NewReader(data),
	)

	if err != nil {
		return err
	}

	req.Header.Set(
		"Authorization",
		"QQBot "+accessToken,
	)

	req.Header.Set(
		"Content-Type",
		"application/json; charset=utf-8",
	)

	req.Header.Set(
		"User-Agent",
		"Qbot/"+Version,
	)

	client := &http.Client{
		Timeout: 20 * time.Second,
	}

	resp, err := client.Do(req)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	responseBody, err := io.ReadAll(
		resp.Body,
	)

	if err != nil {
		return err
	}

	if resp.StatusCode >= 200 &&
		resp.StatusCode < 300 {

		addLog(
			"[API] 消息发送成功 HTTP=%d",
			resp.StatusCode,
		)

		return nil
	}

	var apiErr APIError

	if json.Unmarshal(
		responseBody,
		&apiErr,
	) == nil {

		return fmt.Errorf(
			"QQ API HTTP=%d err_code=%d message=%s trace_id=%s",
			resp.StatusCode,
			apiErr.ErrCode,
			apiErr.Message,
			apiErr.TraceID,
		)
	}

	return fmt.Errorf(
		"QQ API HTTP=%d body=%s",
		resp.StatusCode,
		string(responseBody),
	)
}

//
// ============================================================
// 天气 API
// Open-Meteo
// ============================================================
//

type GeocodeResponse struct {
	Results []GeoResult `json:"results"`
}

type GeoResult struct {
	Name        string  `json:"name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Admin1      string  `json:"admin1"`
}

type WeatherResponse struct {
	Current CurrentWeather `json:"current"`
}

type CurrentWeather struct {
	Time             string  `json:"time"`
	Temperature      float64 `json:"temperature_2m"`
	ApparentTemp     float64 `json:"apparent_temperature"`
	RelativeHumidity float64 `json:"relative_humidity_2m"`
	WindSpeed        float64 `json:"wind_speed_10m"`
	WeatherCode      int     `json:"weather_code"`
}

func weatherCodeText(code int) string {
	switch code {

	case 0:
		return "晴"

	case 1:
		return "大致晴"

	case 2:
		return "局部多云"

	case 3:
		return "阴"

	case 45, 48:
		return "雾"

	case 51, 53, 55:
		return "毛毛雨"

	case 56, 57:
		return "冻毛毛雨"

	case 61, 63, 65:
		return "雨"

	case 66, 67:
		return "冻雨"

	case 71, 73, 75:
		return "雪"

	case 77:
		return "雪粒"

	case 80, 81, 82:
		return "阵雨"

	case 85, 86:
		return "阵雪"

	case 95:
		return "雷暴"

	case 96, 99:
		return "雷暴伴冰雹"

	default:
		return "未知天气"
	}
}

func getWeatherReply(city string) string {
	city = strings.TrimSpace(city)

	if city == "" {
		return "请输入城市，例如：天气 东京"
	}

	configMu.RLock()
	enabled := config.Weather.Enabled
	configMu.RUnlock()

	if !enabled {
		return "天气功能当前已关闭。"
	}

	addLog(
		"[WEATHER] 查询城市: %s",
		city,
	)

	geoURL, err := url.Parse(
		WeatherGeocodeURL,
	)

	if err != nil {
		return "天气服务地址错误。"
	}

	query := geoURL.Query()

	query.Set(
		"name",
		city,
	)

	query.Set(
		"count",
		"1",
	)

	query.Set(
		"language",
		"zh",
	)

	query.Set(
		"format",
		"json",
	)

	geoURL.RawQuery = query.Encode()

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	resp, err := client.Get(
		geoURL.String(),
	)

	if err != nil {
		addLog(
			"[WEATHER] 地理位置查询失败: %v",
			err,
		)

		return "天气服务暂时无法连接，请稍后再试。"
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(
		resp.Body,
	)

	if err != nil {
		return "读取天气服务失败。"
	}

	if resp.StatusCode != http.StatusOK {
		addLog(
			"[WEATHER] 地理位置 HTTP=%d",
			resp.StatusCode,
		)

		return "天气服务返回错误。"
	}

	var geo GeocodeResponse

	if err := json.Unmarshal(
		body,
		&geo,
	); err != nil {

		addLog(
			"[WEATHER] 地理位置 JSON 错误: %v",
			err,
		)

		return "天气服务数据解析失败。"
	}

	if len(geo.Results) == 0 {
		return fmt.Sprintf(
			"没有找到城市「%s」，请换一个城市名称。",
			city,
		)
	}

	location := geo.Results[0]

	weatherURL, err := url.Parse(
		WeatherAPIURL,
	)

	if err != nil {
		return "天气服务地址错误。"
	}

	params := weatherURL.Query()

	params.Set(
		"latitude",
		strconv.FormatFloat(
			location.Latitude,
			'f',
			6,
			64,
		),
	)

	params.Set(
		"longitude",
		strconv.FormatFloat(
			location.Longitude,
			'f',
			6,
			64,
		),
	)

	params.Set(
		"current",
		"temperature_2m,apparent_temperature,relative_humidity_2m,wind_speed_10m,weather_code",
	)

	params.Set(
		"timezone",
		"auto",
	)

	weatherURL.RawQuery = params.Encode()

	resp2, err := client.Get(
		weatherURL.String(),
	)

	if err != nil {
		addLog(
			"[WEATHER] 天气查询失败: %v",
			err,
		)

		return "天气服务暂时无法连接，请稍后再试。"
	}

	defer resp2.Body.Close()

	body2, err := io.ReadAll(
		resp2.Body,
	)

	if err != nil {
		return "读取天气数据失败。"
	}

	if resp2.StatusCode != http.StatusOK {
		addLog(
			"[WEATHER] 天气 HTTP=%d",
			resp2.StatusCode,
		)

		return "天气服务返回错误。"
	}

	var weather WeatherResponse

	if err := json.Unmarshal(
		body2,
		&weather,
	); err != nil {

		addLog(
			"[WEATHER] 天气 JSON 错误: %v",
			err,
		)

		return "天气数据解析失败。"
	}

	current := weather.Current

	placeName := location.Name

	if location.Admin1 != "" &&
		location.Admin1 != location.Name {

		placeName += " · " + location.Admin1
	}

	return fmt.Sprintf(
		"🌤 %s 天气\n\n天气：%s\n温度：%.1f°C\n体感：%.1f°C\n湿度：%.0f%%\n风速：%.1f km/h",
		placeName,
		weatherCodeText(current.WeatherCode),
		current.Temperature,
		current.ApparentTemp,
		current.RelativeHumidity,
		current.WindSpeed,
	)
}

//
// ============================================================
// Web 管理后台
// ============================================================
//

func startWebServer() {
	mux := http.NewServeMux()

	mux.HandleFunc(
		"/",
		webIndex,
	)

	mux.HandleFunc(
		"/api/status",
		webStatus,
	)

	mux.HandleFunc(
		"/api/start",
		webStart,
	)

	mux.HandleFunc(
		"/api/stop",
		webStop,
	)

	mux.HandleFunc(
		"/api/logs",
		webLogs,
	)

	mux.HandleFunc(
		"/api/config",
		webConfig,
	)

	mux.HandleFunc(
		"/api/config/save",
		webConfigSave,
	)

	mux.HandleFunc(
		"/api/replies",
		webReplies,
	)

	mux.HandleFunc(
		"/api/replies/add",
		webReplyAdd,
	)

	mux.HandleFunc(
		"/api/replies/delete",
		webReplyDelete,
	)

	mux.HandleFunc(
		"/api/replies/toggle",
		webReplyToggle,
	)

	address :=
		WebHost +
			":" +
			strconv.Itoa(WebPort)

	addLog(
		"[WEB] 管理后台: http://%s",
		address,
	)

	err := http.ListenAndServe(
		address,
		mux,
	)

	if err != nil {
		log.Fatal(err)
	}
}

//
// ============================================================
// Web 首页
// ============================================================
//

func webIndex(
	w http.ResponseWriter,
	r *http.Request,
) {

	w.Header().Set(
		"Content-Type",
		"text/html; charset=utf-8",
	)

	fmt.Fprint(
		w,
		webHTML,
	)
}

//
// ============================================================
// Web Status
// ============================================================
//

func webStatus(
	w http.ResponseWriter,
	r *http.Request,
) {

	stateMu.RLock()

	status := map[string]interface{}{
		"version":             Version,
		"running":             running,
		"ready":               ready,
		"app_id":              appID,
		"bot_id":              botID,
		"bot_name":            botName,
		"session_id":          sessionID,
		"sequence":            sequence,
		"heartbeat_interval":  heartbeatInterval.String(),
		"last_heartbeat":      lastHeartbeat,
		"last_heartbeat_ack":  lastHeartbeatACK,
		"last_connect":        lastConnect,
		"last_disconnect":     lastDisconnect,
		"reconnect_count":     reconnectCount,
	}

	stateMu.RUnlock()

	configMu.RLock()

	status["weather_enabled"] =
		config.Weather.Enabled

	status["weather_city"] =
		config.Weather.DefaultCity

	configMu.RUnlock()

	writeJSON(
		w,
		status,
	)
}

//
// ============================================================
// Web Start / Stop
// ============================================================
//

func webStart(
	w http.ResponseWriter,
	r *http.Request,
) {

	startGateway()

	writeJSON(
		w,
		map[string]interface{}{
			"ok": true,
		},
	)
}

func webStop(
	w http.ResponseWriter,
	r *http.Request,
) {

	stopGateway()

	writeJSON(
		w,
		map[string]interface{}{
			"ok": true,
		},
	)
}

//
// ============================================================
// Web Logs
// ============================================================
//

func webLogs(
	w http.ResponseWriter,
	r *http.Request,
) {

	writeJSON(
		w,
		map[string]interface{}{
			"logs": getLogs(),
		},
	)
}

//
// ============================================================
// Web Config GET
// ============================================================
//

func webConfig(
	w http.ResponseWriter,
	r *http.Request,
) {

	configMu.RLock()

	result := map[string]interface{}{
		"app_id": appID,

		"weather": config.Weather,
	}

	configMu.RUnlock()

	writeJSON(
		w,
		result,
	)
}

//
// ============================================================
// Web Config SAVE
// ============================================================
//

type ConfigSaveRequest struct {
	AppID         string `json:"app_id"`
	AppSecret     string `json:"app_secret"`
	WeatherEnable bool   `json:"weather_enabled"`
	DefaultCity   string `json:"default_city"`
}

func webConfigSave(
	w http.ResponseWriter,
	r *http.Request,
) {

	if r.Method != http.MethodPost {
		writeJSON(
			w,
			map[string]interface{}{
				"ok":    false,
				"error": "只支持 POST",
			},
		)

		return
	}

	var req ConfigSaveRequest

	if err := json.NewDecoder(
		r.Body,
	).Decode(&req); err != nil {

		writeJSON(
			w,
			map[string]interface{}{
				"ok":    false,
				"error": "JSON 格式错误",
			},
		)

		return
	}

	configMu.Lock()

	if strings.TrimSpace(req.AppID) != "" {
		appID = strings.TrimSpace(req.AppID)
	}

	if strings.TrimSpace(req.AppSecret) != "" {
		appSecret = strings.TrimSpace(req.AppSecret)
	}

	config.AppID = appID
	config.AppSecret = appSecret

	config.Weather.Enabled =
		req.WeatherEnable

	if strings.TrimSpace(
		req.DefaultCity,
	) != "" {

		config.Weather.DefaultCity =
			strings.TrimSpace(
				req.DefaultCity,
			)
	}

	err := saveConfigLocked()

	configMu.Unlock()

	if err != nil {

		addLog(
			"[CONFIG] 保存失败: %v",
			err,
		)

		writeJSON(
			w,
			map[string]interface{}{
				"ok":    false,
				"error": err.Error(),
			},
		)

		return
	}

	addLog(
		"[CONFIG] AppID / AppSecret 已保存",
	)

	writeJSON(
		w,
		map[string]interface{}{
			"ok": true,
		},
	)
}

//
// ============================================================
// 自定义回复 GET
// ============================================================
//

func webReplies(
	w http.ResponseWriter,
	r *http.Request,
) {

	configMu.RLock()

	result := append(
		[]CustomReply(nil),
		config.Replies...,
	)

	configMu.RUnlock()

	writeJSON(
		w,
		map[string]interface{}{
			"replies": result,
		},
	)
}

//
// ============================================================
// 添加回复
// ============================================================
//

type ReplyAddRequest struct {
	Trigger string `json:"trigger"`
	Reply   string `json:"reply"`
	Mode    string `json:"mode"`
	Enabled bool   `json:"enabled"`
}

func webReplyAdd(
	w http.ResponseWriter,
	r *http.Request,
) {

	if r.Method != http.MethodPost {

		writeJSON(
			w,
			map[string]interface{}{
				"ok":    false,
				"error": "只支持 POST",
			},
		)

		return
	}

	var req ReplyAddRequest

	if err := json.NewDecoder(
		r.Body,
	).Decode(&req); err != nil {

		writeJSON(
			w,
			map[string]interface{}{
				"ok":    false,
				"error": "JSON 格式错误",
			},
		)

		return
	}

	req.Trigger = strings.TrimSpace(
		req.Trigger,
	)

	req.Reply = strings.TrimSpace(
		req.Reply,
	)

	if req.Trigger == "" {
		writeJSON(
			w,
			map[string]interface{}{
				"ok":    false,
				"error": "触发词不能为空",
			},
		)

		return
	}

	if req.Reply == "" {
		writeJSON(
			w,
			map[string]interface{}{
				"ok":    false,
				"error": "回复内容不能为空",
			},
		)

		return
	}

	if req.Mode != "contains" {
		req.Mode = "exact"
	}

	item := CustomReply{
		ID:      time.Now().UnixNano(),
		Trigger: req.Trigger,
		Reply:   req.Reply,
		Mode:    req.Mode,
		Enabled: req.Enabled,
	}

	configMu.Lock()

	config.Replies = append(
		config.Replies,
		item,
	)

	err := saveConfigLocked()

	configMu.Unlock()

	if err != nil {

		writeJSON(
			w,
			map[string]interface{}{
				"ok":    false,
				"error": err.Error(),
			},
		)

		return
	}

	addLog(
		"[REPLY] 添加规则: %s -> %s",
		item.Trigger,
		item.Reply,
	)

	writeJSON(
		w,
		map[string]interface{}{
			"ok":    true,
			"reply": item,
		},
	)
}

//
// ============================================================
// 删除回复
// ============================================================
//

func webReplyDelete(
	w http.ResponseWriter,
	r *http.Request,
) {

	if r.Method != http.MethodPost {

		writeJSON(
			w,
			map[string]interface{}{
				"ok": false,
			},
		)

		return
	}

	var req struct {
		ID int64 `json:"id"`
	}

	if err := json.NewDecoder(
		r.Body,
	).Decode(&req); err != nil {

		writeJSON(
			w,
			map[string]interface{}{
				"ok":    false,
				"error": "JSON 格式错误",
			},
		)

		return
	}

	configMu.Lock()

	found := false

	newReplies := make(
		[]CustomReply,
		0,
		len(config.Replies),
	)

	for _, item := range config.Replies {

		if item.ID == req.ID {
			found = true
			continue
		}

		newReplies = append(
			newReplies,
			item,
		)
	}

	config.Replies = newReplies

	var err error

	if found {
		err = saveConfigLocked()
	}

	configMu.Unlock()

	if err != nil {

		writeJSON(
			w,
			map[string]interface{}{
				"ok":    false,
				"error": err.Error(),
			},
		)

		return
	}

	writeJSON(
		w,
		map[string]interface{}{
			"ok":      true,
			"deleted": found,
		},
	)
}

//
// ============================================================
// 开关回复
// ============================================================
//

func webReplyToggle(
	w http.ResponseWriter,
	r *http.Request,
) {

	if r.Method != http.MethodPost {

		writeJSON(
			w,
			map[string]interface{}{
				"ok": false,
			},
		)

		return
	}

	var req struct {
		ID      int64 `json:"id"`
		Enabled bool  `json:"enabled"`
	}

	if err := json.NewDecoder(
		r.Body,
	).Decode(&req); err != nil {

		writeJSON(
			w,
			map[string]interface{}{
				"ok":    false,
				"error": "JSON 格式错误",
			},
		)

		return
	}

	configMu.Lock()

	found := false

	for i := range config.Replies {

		if config.Replies[i].ID == req.ID {

			config.Replies[i].Enabled =
				req.Enabled

			found = true

			break
		}
	}

	var err error

	if found {
		err = saveConfigLocked()
	}

	configMu.Unlock()

	if err != nil {

		writeJSON(
			w,
			map[string]interface{}{
				"ok":    false,
				"error": err.Error(),
			},
		)

		return
	}

	writeJSON(
		w,
		map[string]interface{}{
			"ok":      true,
			"updated": found,
		},
	)
}

//
// ============================================================
// JSON
// ============================================================
//

func writeJSON(
	w http.ResponseWriter,
	data interface{},
) {

	w.Header().Set(
		"Content-Type",
		"application/json; charset=utf-8",
	)

	_ = json.NewEncoder(
		w,
	).Encode(data)
}

//
// ============================================================
// Web HTML
// ============================================================
//

const webHTML = `
<!DOCTYPE html>
<html lang="zh-CN">

<head>

<meta charset="UTF-8">

<meta
 name="viewport"
 content="width=device-width,initial-scale=1"
>

<title>Qbot v0.2.1</title>

<style>

* {
 box-sizing: border-box;
}

body {
 margin: 0;
 background: #0f1117;
 color: #e6e6e6;
 font-family:
 -apple-system,
 BlinkMacSystemFont,
 "Segoe UI",
 "Microsoft YaHei",
 sans-serif;
}

.header {
 padding: 20px 25px;
 background: #171a22;
 border-bottom: 1px solid #292d38;
}

.header h1 {
 margin: 0;
 font-size: 24px;
}

.header p {
 margin: 7px 0 0;
 color: #8e96a3;
}

.container {
 max-width: 1150px;
 margin: 25px auto;
 padding: 0 18px;
}

.section {
 margin-top: 20px;
 background: #171a22;
 border: 1px solid #292d38;
 border-radius: 12px;
 padding: 20px;
}

.section h2 {
 margin-top: 0;
 font-size: 18px;
}

.cards {
 display: grid;
 grid-template-columns:
 repeat(auto-fit,minmax(190px,1fr));
 gap: 15px;
}

.card {
 background: #171a22;
 border: 1px solid #292d38;
 border-radius: 12px;
 padding: 18px;
}

.card-title {
 color: #8e96a3;
 font-size: 13px;
 margin-bottom: 8px;
}

.card-value {
 font-size: 20px;
 font-weight: 600;
}

.online {
 color: #49d17d;
}

.offline {
 color: #ff5f5f;
}

.connecting {
 color: #f1c75b;
}

.buttons {
 margin-top: 20px;
 display: flex;
 flex-wrap: wrap;
 gap: 10px;
}

button {
 border: 0;
 border-radius: 8px;
 padding: 10px 18px;
 font-size: 14px;
 cursor: pointer;
}

button:hover {
 opacity: .85;
}

.start {
 background: #2ea043;
 color: white;
}

.stop {
 background: #da3633;
 color: white;
}

.refresh {
 background: #30363d;
 color: white;
}

.save {
 background: #238636;
 color: white;
}

.delete {
 background: #da3633;
 color: white;
}

input,
textarea,
select {
 width: 100%;
 background: #0d1117;
 color: #e6e6e6;
 border: 1px solid #30363d;
 border-radius: 7px;
 padding: 10px;
 margin-top: 6px;
 font-size: 14px;
}

textarea {
 min-height: 80px;
 resize: vertical;
}

label {
 display: block;
 margin-top: 12px;
 color: #c9d1d9;
 font-size: 13px;
}

.check {
 display: flex;
 align-items: center;
 gap: 8px;
 margin-top: 15px;
}

.check input {
 width: auto;
 margin: 0;
}

.info {
 display: grid;
 grid-template-columns:
 repeat(auto-fit,minmax(280px,1fr));
 gap: 15px;
 margin-top: 20px;
}

.info div {
 background: #171a22;
 border: 1px solid #292d38;
 border-radius: 12px;
 padding: 15px;
}

.label {
 color: #8e96a3;
 font-size: 12px;
}

.value {
 margin-top: 5px;
 word-break: break-all;
}

.logs {
 margin-top: 20px;
 background: #090b10;
 border: 1px solid #292d38;
 border-radius: 12px;
 padding: 15px;
}

.logs-title {
 font-size: 16px;
 margin-bottom: 10px;
}

#log {
 height: 400px;
 overflow-y: auto;
 font-family: Consolas, monospace;
 font-size: 13px;
 white-space: pre-wrap;
 color: #c9d1d9;
}

.reply-table {
 width: 100%;
 border-collapse: collapse;
 margin-top: 15px;
}

.reply-table th,
.reply-table td {
 border-bottom: 1px solid #30363d;
 padding: 10px;
 text-align: left;
 vertical-align: top;
}

.reply-table th {
 color: #8e96a3;
 font-size: 12px;
}

.reply-reply {
 white-space: pre-wrap;
 word-break: break-word;
 max-width: 400px;
}

.badge {
 display: inline-block;
 padding: 3px 8px;
 border-radius: 20px;
 background: #30363d;
 font-size: 12px;
}

.badge-on {
 background: #238636;
}

.badge-off {
 background: #da3633;
}

.small {
 color: #8e96a3;
 font-size: 12px;
 margin-top: 7px;
}

</style>

</head>

<body>

<div class="header">

<h1>Qbot v0.2.1</h1>

<p>QQ Official Bot · WebSocket · 自动重连 · 心跳 · 自定义回复 · 天气</p>

</div>

<div class="container">

<div class="cards">

<div class="card">

<div class="card-title">
运行状态
</div>

<div id="running" class="card-value">
停止
</div>

</div>

<div class="card">

<div class="card-title">
QQ Gateway
</div>

<div id="ready" class="card-value">
未连接
</div>

</div>

<div class="card">

<div class="card-title">
Bot
</div>

<div id="bot" class="card-value">
-
</div>

</div>

<div class="card">

<div class="card-title">
心跳
</div>

<div id="heartbeat" class="card-value">
-
</div>

</div>

</div>

<div class="buttons">

<button
 class="start"
 onclick="startBot()"
>
启动 Bot
</button>

<button
 class="stop"
 onclick="stopBot()"
>
停止 Bot
</button>

<button
 class="refresh"
 onclick="refreshAll()"
>
刷新
</button>

</div>

<div class="info">

<div>
<div class="label">AppID</div>
<div id="appid" class="value">-</div>
</div>

<div>
<div class="label">Session ID</div>
<div id="session" class="value">-</div>
</div>

<div>
<div class="label">Sequence</div>
<div id="sequence" class="value">-</div>
</div>

<div>
<div class="label">重连次数</div>
<div id="reconnect" class="value">0</div>
</div>

</div>


<div class="section">

<h2>⚙ QQ / 天气设置</h2>

<label>
AppID
<input
 id="appID"
 type="text"
 placeholder="请输入 QQ Bot AppID"
>
</label>

<label>
AppSecret
<input
 id="appSecret"
 type="password"
 placeholder="请输入 AppSecret"
>
</label>

<label class="check">

<input
 id="weatherEnabled"
 type="checkbox"
>

<span>启用天气功能</span>

</label>

<label>
默认城市
<input
 id="defaultCity"
 type="text"
 placeholder="例如：东京"
>
</label>

<div class="small">
天气使用 Open-Meteo，无需填写天气 API Key。
</div>

<div class="buttons">

<button
 class="save"
 onclick="saveConfig()"
>
保存设置
</button>

</div>

</div>


<div class="section">

<h2>💬 自定义回复</h2>

<p class="small">
例如：触发词填写「xxx九」，回复填写「yyy」。
</p>

<label>
触发词
<input
 id="replyTrigger"
 type="text"
 placeholder="例如：xxx九"
>
</label>

<label>
回复内容
<textarea
 id="replyText"
 placeholder="例如：yyy"
></textarea>
</label>

<label>
匹配方式
<select id="replyMode">

<option value="exact">
完全匹配
</option>

<option value="contains">
包含关键词
</option>

</select>
</label>

<label class="check">

<input
 id="replyEnabled"
 type="checkbox"
 checked
>

<span>启用此回复</span>

</label>

<div class="buttons">

<button
 class="save"
 onclick="addReply()"
>
添加回复
</button>

</div>

<div id="replyList">
正在加载...
</div>

</div>


<div class="logs">

<div class="logs-title">
实时日志
</div>

<div id="log">
正在加载...
</div>

</div>

</div>


<script>

async function api(url, options) {

 const response =
 await fetch(url, options || {});

 return await response.json();

}


async function refreshStatus() {

 try {

 const data =
 await api("/api/status");

 const running =
 document.getElementById("running");

 const ready =
 document.getElementById("ready");


 if (data.running) {

 running.textContent =
 "运行中";

 running.className =
 "card-value online";

 } else {

 running.textContent =
 "停止";

 running.className =
 "card-value offline";

 }


 if (data.ready) {

 ready.textContent =
 "已连接";

 ready.className =
 "card-value online";

 } else if (data.running) {

 ready.textContent =
 "连接中";

 ready.className =
 "card-value connecting";

 } else {

 ready.textContent =
 "未连接";

 ready.className =
 "card-value offline";

 }


 document.getElementById("bot")
 .textContent =
 data.bot_name || "-";


 document.getElementById("appid")
 .textContent =
 data.app_id || "-";


 document.getElementById("session")
 .textContent =
 data.session_id || "-";


 document.getElementById("sequence")
 .textContent =
 data.sequence;


 document.getElementById("heartbeat")
 .textContent =
 data.last_heartbeat || "-";


 document.getElementById("reconnect")
 .textContent =
 data.reconnect_count;


 } catch(e) {

 console.error(e);

 }

}


async function refreshLogs() {

 try {

 const data =
 await api("/api/logs");

 const log =
 document.getElementById("log");

 log.textContent =
 data.logs.join("\n");

 log.scrollTop =
 log.scrollHeight;

 } catch(e) {

 console.error(e);

 }

}


async function loadConfig() {

 try {

 const data =
 await api("/api/config");

 document.getElementById("appID")
 .value =
 data.app_id || "";

 document.getElementById("weatherEnabled")
 .checked =
 !!data.weather.enabled;

 document.getElementById("defaultCity")
 .value =
 data.weather.default_city || "东京";

 } catch(e) {

 console.error(e);

 }

}


async function saveConfig() {

 const body = {

 app_id:
 document.getElementById("appID").value,

 app_secret:
 document.getElementById("appSecret").value,

 weather_enabled:
 document.getElementById("weatherEnabled").checked,

 default_city:
 document.getElementById("defaultCity").value

 };

 const data =
 await api(
 "/api/config/save",
 {
 method: "POST",

 headers: {
 "Content-Type":
 "application/json"
 },

 body:
 JSON.stringify(body)
 }
 );

 if (data.ok) {

 alert("设置保存成功");

 document.getElementById("appSecret")
 .value = "";

 } else {

 alert(
 "保存失败：" +
 (data.error || "未知错误")
 );

 }

}


async function addReply() {

 const trigger =
 document.getElementById("replyTrigger").value;

 const reply =
 document.getElementById("replyText").value;

 const mode =
 document.getElementById("replyMode").value;

 const enabled =
 document.getElementById("replyEnabled").checked;


 if (!trigger.trim()) {

 alert("请输入触发词");

 return;

 }


 if (!reply.trim()) {

 alert("请输入回复内容");

 return;

 }


 const data =
 await api(
 "/api/replies/add",
 {
 method: "POST",

 headers: {
 "Content-Type":
 "application/json"
 },

 body:
 JSON.stringify({
 trigger,
 reply,
 mode,
 enabled
 })
 }
 );


 if (data.ok) {

 document.getElementById("replyTrigger")
 .value = "";

 document.getElementById("replyText")
 .value = "";

 await refreshReplies();

 } else {

 alert(
 "添加失败：" +
 (data.error || "未知错误")
 );

 }

}


async function deleteReply(id) {

 if (!confirm("确定删除这条回复规则吗？")) {
 return;
 }


 const data =
 await api(
 "/api/replies/delete",
 {
 method: "POST",

 headers: {
 "Content-Type":
 "application/json"
 },

 body:
 JSON.stringify({
 id
 })
 }
 );


 if (!data.ok) {

 alert("删除失败");

 }


 await refreshReplies();

}


async function toggleReply(
 id,
 enabled
) {

 const data =
 await api(
 "/api/replies/toggle",
 {
 method: "POST",

 headers: {
 "Content-Type":
 "application/json"
 },

 body:
 JSON.stringify({
 id,
 enabled
 })
 }
 );


 if (!data.ok) {

 alert(
 "修改失败：" +
 (data.error || "未知错误")
 );

 }


 await refreshReplies();

}


async function refreshReplies() {

 try {

 const data =
 await api("/api/replies");

 const list =
 document.getElementById("replyList");


 if (!data.replies ||
 data.replies.length === 0) {

 list.innerHTML =
 "<p class='small'>暂无自定义回复。</p>";

 return;

 }


 let html =
 "<table class='reply-table'>";

 html +=
 "<tr>" +
 "<th>触发词</th>" +
 "<th>回复</th>" +
 "<th>模式</th>" +
 "<th>状态</th>" +
 "<th>操作</th>" +
 "</tr>";


 for (const item of data.replies) {

 const status =
 item.enabled
 ? "<span class='badge badge-on'>启用</span>"
 : "<span class='badge badge-off'>关闭</span>";

 const mode =
 item.mode === "contains"
 ? "包含"
 : "完全匹配";


 html +=
 "<tr>";

 html +=
 "<td>" +
 escapeHTML(item.trigger) +
 "</td>";

 html +=
 "<td class='reply-reply'>" +
 escapeHTML(item.reply) +
 "</td>";

 html +=
 "<td>" +
 mode +
 "</td>";

 html +=
 "<td>" +
 status +
 "</td>";

 html +=
 "<td>" +

 "<button class='refresh' " +
 "onclick='toggleReply(" +
 item.id +
 "," +
 (!item.enabled) +
 ")'>" +
 (item.enabled ? "关闭" : "启用") +
 "</button> " +

 "<button class='delete' " +
 "onclick='deleteReply(" +
 item.id +
 ")'>" +
 "删除" +
 "</button>" +

 "</td>";

 html +=
 "</tr>";

 }


 html +=
 "</table>";

 list.innerHTML =
 html;

 } catch(e) {

 console.error(e);

 }

}


function escapeHTML(text) {

 return String(text)
 .replaceAll("&", "&amp;")
 .replaceAll("<", "&lt;")
 .replaceAll(">", "&gt;")
 .replaceAll('"', "&quot;")
 .replaceAll("'", "&#039;");

}


async function startBot() {

 const data =
 await api(
 "/api/start",
 {
 method: "POST"
 }
 );

 if (!data.ok) {
 alert("启动失败");
 }

 await refreshAll();

}


async function stopBot() {

 await api(
 "/api/stop",
 {
 method: "POST"
 }
 );

 await refreshAll();

}


async function refreshAll() {

 await refreshStatus();

 await refreshLogs();

 await refreshReplies();

}


loadConfig();

refreshAll();

setInterval(
 refreshAll,
 2000
);

</script>

</body>

</html>
`

//
// ============================================================
// 辅助
// ============================================================
//

func sleepOrStop(
	duration time.Duration,
) {

	timer := time.NewTimer(duration)

	defer timer.Stop()

	select {

	case <-timer.C:
		return

	case <-stopBotChannel():
		return
	}
}

//
// ============================================================
// 信号
// ============================================================
//

func setupSignalHandler() {

	ch := make(
		chan os.Signal,
		1,
	)

	signal.Notify(
		ch,
		os.Interrupt,
		syscall.SIGTERM,
	)

	go func() {

		<-ch

		addLog(
			"[SYSTEM] 正在退出",
		)

		stopGateway()

		time.Sleep(
			300 * time.Millisecond,
		)

		os.Exit(0)

	}()
}

//
// ============================================================
// main
// ============================================================
//

func main() {

	log.SetFlags(
		log.LstdFlags |
			log.Lmicroseconds,
	)

	fmt.Println()
	fmt.Println("======================================")
	fmt.Println(" Qbot", Version)
	fmt.Println(" QQ Official Bot")
	fmt.Println(" WebSocket + Web Manager")
	fmt.Println(" 自定义回复 + 天气 API")
	fmt.Println("======================================")
	fmt.Println()

	//
	// 加载配置
	//

	if err := loadPersistentConfig(); err != nil {

		fmt.Println(
			"[ERROR] 配置加载失败:",
			err,
		)

		return
	}

	addLog(
		"[SYSTEM] Qbot %s 启动",
		Version,
	)

	if appID == "" ||
		appSecret == "" {

		addLog(
			"[SYSTEM] 请打开 Web 管理后台配置 AppID / AppSecret",
		)

	} else {

		addLog(
			"[SYSTEM] AppID 已配置",
		)
	}

	setupSignalHandler()

	//
	// Web 后台始终启动
	//

	go startWebServer()

	//
	// 如果已经配置 AppID / Secret
	// 自动启动 Bot
	//

	time.Sleep(
		300 * time.Millisecond,
	)

	configMu.RLock()

	configured :=
		appID != "" &&
			appSecret != ""

	configMu.RUnlock()

	if configured {

		startGateway()

	} else {

		addLog(
			"[SYSTEM] 等待 Web 后台配置",
		)

	}

	//
	// 保持程序运行
	//

	select {}
}
