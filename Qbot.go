package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
)

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

	logMu    sync.Mutex
	logLines []string

	stopBotChan chan struct{}

	configMu sync.Mutex
)

type Config struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
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
// 配置
// ============================================================
//

func loadConfig() {
	configMu.Lock()
	defer configMu.Unlock()

	data, err := os.ReadFile(ConfigFile)
	if err != nil {
		if os.IsNotExist(err) {
			addLog("[CONFIG] 尚未配置 AppID / AppSecret")
		} else {
			addLog("[CONFIG] 读取配置失败: %v", err)
		}
		return
	}

	var cfg Config

	if err := json.Unmarshal(data, &cfg); err != nil {
		addLog("[CONFIG] 配置文件解析失败: %v", err)
		return
	}

	appID = strings.TrimSpace(cfg.AppID)
	appSecret = strings.TrimSpace(cfg.AppSecret)

	if appID != "" {
		addLog("[CONFIG] AppID 已加载")
	}

	if appSecret != "" {
		addLog("[CONFIG] AppSecret 已加载")
	}
}

func saveConfig(newAppID, newAppSecret string) error {
	newAppID = strings.TrimSpace(newAppID)
	newAppSecret = strings.TrimSpace(newAppSecret)

	if newAppID == "" {
		return fmt.Errorf("AppID 不能为空")
	}

	if newAppSecret == "" {
		return fmt.Errorf("AppSecret 不能为空")
	}

	cfg := Config{
		AppID:     newAppID,
		AppSecret: newAppSecret,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	configMu.Lock()
	defer configMu.Unlock()

	if err := os.WriteFile(ConfigFile, data, 0600); err != nil {
		return err
	}

	appID = newAppID
	appSecret = newAppSecret

	accessToken = ""
	tokenExpire = time.Time{}

	addLog("[CONFIG] AppID / AppSecret 已保存")

	return nil
}

//
// ============================================================
// Token
// ============================================================
//

type TokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   string `json:"expires_in"`
}

func getAccessToken() error {
	configMu.Lock()
	id := appID
	secret := appSecret
	configMu.Unlock()

	if id == "" || secret == "" {
		return fmt.Errorf("请先在 Web 管理后台配置 AppID 和 AppSecret")
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

	req.Header.Set("Content-Type", "application/json")

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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf(
			"Token HTTP %d: %s",
			resp.StatusCode,
			string(body),
		)
	}

	var result TokenResponse

	if err := json.Unmarshal(body, &result); err != nil {
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

	accessToken = result.AccessToken

	expiresIn, err := strconv.ParseInt(
		strings.TrimSpace(result.ExpiresIn),
			10,
			64,
	)

	if err != nil || expiresIn <= 0 {
		expiresIn = 7200
	}

	tokenExpire = time.Now().Add(
		time.Duration(expiresIn) * time.Second,
	)

	addLog(
		"[QQ] Access Token 获取成功，有效期 %d 秒",
		expiresIn,
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

	if err := json.Unmarshal(body, &result); err != nil {
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
// Gateway Packet
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
    Intents    int64             `json:"intents"`
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

const (
	IntentGuildMessages        = 1 << 9
	IntentDirectMessage        = 1 << 12
	IntentC2CAndGroup          = 1 << 25
)

//
// ============================================================
// 启动 / 停止
// ============================================================
//

func startGateway() {
	stateMu.Lock()

	if running {
		stateMu.Unlock()

		addLog("[BOT] 已经在运行")
		return
	}

	if strings.TrimSpace(appID) == "" ||
		strings.TrimSpace(appSecret) == "" {

		stateMu.Unlock()

		addLog("[BOT] 未配置 AppID / AppSecret")
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

//
// ============================================================
// 自动重连
// ============================================================
//

func gatewayLoop() {
	delay := ReconnectMin

	for {
		if !isRunning() {
			return
		}

		if accessToken == "" ||
			time.Now().After(tokenExpire.Add(-2*time.Minute)) {

			if err := getAccessToken(); err != nil {
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
// WebSocket
// ============================================================
//

func connectGateway(url string) error {
	addLog("[GATEWAY] 正在连接 WebSocket")

	conn, _, err := websocket.DefaultDialer.Dial(
		url,
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

		if err := json.Unmarshal(data, &packet); err != nil {
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

		if err := handlePacket(packet); err != nil {
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
// Packet
// ============================================================
//

func handlePacket(packet GatewayPacket) error {
	switch packet.Op {

	case 0:
		return handleDispatch(packet)

	case 1:
		addLog("[GATEWAY] 收到 Heartbeat 请求")
		return sendHeartbeat()

	case 7:
		addLog("[GATEWAY] 服务器要求重新连接")
		return fmt.Errorf("server requested reconnect")

	case 9:
		addLog("[GATEWAY] Invalid Session")
		return fmt.Errorf("invalid session")

	case 10:
		return handleHello(packet)

	case 11:
		stateMu.Lock()
		lastHeartbeatACK = time.Now()
		stateMu.Unlock()

		addLog("[GATEWAY] Heartbeat ACK")

	default:
		addLog(
			"[GATEWAY] 未处理 Opcode=%d",
			packet.Op,
		)
	}

	return nil
}

func handleHello(packet GatewayPacket) error {
	var hello HelloData

	if err := json.Unmarshal(packet.D, &hello); err != nil {
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

	go heartbeatLoop(interval)

	return nil
}

func sendIdentify() error {
	// QQ C2C 单聊 + 群聊 @机器人
	//
	// 1 << 25:
	// C2C_MESSAGE_CREATE
	// GROUP_AT_MESSAGE_CREATE
	//
	// 如果你的 QQ 开放平台后台同时开启了频道消息，
	// 再额外加入 1 << 30。
	var intents int64 = 1 << 25

	data := IdentifyData{
		// 非常重要：
		// QQ Gateway 要求这里是：
		// QQBot {AccessToken}
		Token: "QQBot " + accessToken,

		Intents: intents,

		// 单连接必须是 [0, 1]
		// 含义：当前 shard = 0，总 shard = 1
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

	err := sendPacket(
		2,
		data,
	)

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

func heartbeatLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {

		case <-ticker.C:

			if !isRunning() {
				return
			}

			if err := sendHeartbeat(); err != nil {
				addLog(
					"[HEARTBEAT] 发送失败: %v",
					err,
				)

				closeWebSocket()
				return
			}

		case <-stopBotChannel():
			return
		}
	}
}

func stopBotChannel() <-chan struct{} {
	stateMu.RLock()
	ch := stopBotChan
	stateMu.RUnlock()

	if ch == nil {
		return make(chan struct{})
	}

	return ch
}

func sendHeartbeat() error {
	stateMu.RLock()
	seq := sequence
	stateMu.RUnlock()

	err := sendPacket(1, seq)

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

func sendPacket(op int, data interface{}) error {
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
		return fmt.Errorf("WebSocket 未连接")
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

func handleDispatch(packet GatewayPacket) error {
	switch packet.T {

	case "READY":
		return handleReady(packet)

	case "RESUMED":

		addLog("[GATEWAY] Session RESUMED")

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
		addLog("[EVENT] %s", packet.T)
	}

	return nil
}

//
// ============================================================
// READY
// ============================================================
//

func handleReady(packet GatewayPacket) error {
	var data ReadyData

	if err := json.Unmarshal(packet.D, &data); err != nil {
		return err
	}

	stateMu.Lock()

	sessionID = data.SessionID
	botID = data.User.ID
	botName = data.User.Username
	ready = true
	reconnectCount = 0

	stateMu.Unlock()

	addLog("======================================")
	addLog("[SUCCESS] QQ Bot 已连接")
	addLog("[BOT] %s (%s)", data.User.Username, data.User.ID)
	addLog("[SESSION] %s", data.SessionID)
	addLog("======================================")

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
	ID          string        `json:"id"`
	Content     string        `json:"content"`
	Timestamp   string        `json:"timestamp"`
	Author      MessageAuthor `json:"author"`

	UserOpenID  string `json:"user_openid"`
	GroupOpenID string `json:"group_openid"`

	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
}

func handleC2CMessage(packet GatewayPacket) error {
	var msg QQMessage

	if err := json.Unmarshal(packet.D, &msg); err != nil {
		return err
	}

	if msg.Author.Bot {
		return nil
	}

	userOpenID := msg.UserOpenID

	if userOpenID == "" {
		userOpenID = msg.Author.UserOpenID
	}

	content := strings.TrimSpace(msg.Content)

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

func handleGroupMessage(packet GatewayPacket) error {
	var msg QQMessage

	if err := json.Unmarshal(packet.D, &msg); err != nil {
		return err
	}

	if msg.Author.Bot {
		return nil
	}

	content := strings.TrimSpace(msg.Content)

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

func handleChannelMessage(packet GatewayPacket) error {
	var msg QQMessage

	if err := json.Unmarshal(packet.D, &msg); err != nil {
		return err
	}

	if msg.Author.Bot {
		return nil
	}

	content := strings.TrimSpace(msg.Content)

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

func handleDirectMessage(packet GatewayPacket) error {
	var msg QQMessage

	if err := json.Unmarshal(packet.D, &msg); err != nil {
		return err
	}

	if msg.Author.Bot {
		return nil
	}

	content := strings.TrimSpace(msg.Content)

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
// 回复
// ============================================================
//

func makeReply(content string) string {
	content = strings.TrimSpace(content)

	switch strings.ToLower(content) {

	case "你好":
		return "你好，我是 Qbot v0.2.1"

	case "hello":
		return "Hello!"

	case "hi":
		return "Hi!"

	case "ping", "/ping":
		return "pong"

	case "版本", "/version":
		return "Qbot v0.2.1"

	case "状态", "/status":
		return getBotStatusText()

	case "帮助", "/help":
		return "可用指令：你好、ping、版本、状态、帮助"
	}

	return ""
}

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
// REST API
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
		return fmt.Errorf("user_openid 为空")
	}

	return postAPI(
		APIBaseURL+"/v2/users/"+userOpenID+"/messages",
		SendMessageRequest{
			Content: content,
			MsgType: 0,
			MsgID:   msgID,
		},
	)
}

func sendGroupMessage(
	groupOpenID string,
	content string,
	msgID string,
) error {

	if groupOpenID == "" {
		return fmt.Errorf("group_openid 为空")
	}

	return postAPI(
		APIBaseURL+"/v2/groups/"+groupOpenID+"/messages",
		SendMessageRequest{
			Content: content,
			MsgType: 0,
			MsgID:   msgID,
		},
	)
}

func sendChannelMessage(
	channelID string,
	content string,
	msgID string,
) error {

	if channelID == "" {
		return fmt.Errorf("channel_id 为空")
	}

	return postAPI(
		APIBaseURL+"/v2/channels/"+channelID+"/messages",
		SendMessageRequest{
			Content: content,
			MsgType: 0,
			MsgID:   msgID,
		},
	)
}

func postAPI(url string, body interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(
		http.MethodPost,
		url,
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

	responseBody, err := io.ReadAll(resp.Body)
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

	if json.Unmarshal(responseBody, &apiErr) == nil {
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
// Web API
// ============================================================
//

type WebConfigRequest struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
}

func webConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		configMu.Lock()

		result := map[string]interface{}{
			"app_id":          appID,
			"has_app_secret": appSecret != "",
		}

		configMu.Unlock()

		writeJSON(w, result)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(
			w,
			"Method Not Allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	var req WebConfigRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]interface{}{
			"ok":    false,
			"error": "JSON 格式错误",
		})
		return
	}

	if err := saveConfig(
		req.AppID,
		req.AppSecret,
	); err != nil {

		writeJSON(w, map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
		})

		return
	}

	writeJSON(w, map[string]interface{}{
		"ok": true,
	})
}

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
		"has_app_secret":      appSecret != "",
	}

	stateMu.RUnlock()

	writeJSON(w, status)
}

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
// Web Server
// ============================================================
//

func startWebServer() {
	mux := http.NewServeMux()

	mux.HandleFunc("/", webIndex)
	mux.HandleFunc("/api/status", webStatus)
	mux.HandleFunc("/api/config", webConfig)
	mux.HandleFunc("/api/start", webStart)
	mux.HandleFunc("/api/stop", webStop)
	mux.HandleFunc("/api/logs", webLogs)

	address := WebHost + ":" + strconv.Itoa(WebPort)

	addLog(
		"[WEB] 管理后台: http://%s",
		address,
	)

	err := http.ListenAndServe(
		address,
		mux,
	)

	if err != nil {
		log.Println("[WEB] 服务器停止:", err)
	}
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

<meta name="viewport"
content="width=device-width,initial-scale=1">

<title>Qbot v0.2.1</title>

<style>

* {
	box-sizing:border-box;
}

body {
	margin:0;
	background:#0f1117;
	color:#e6e6e6;
	font-family:
		-apple-system,
		BlinkMacSystemFont,
		"Segoe UI",
		"Microsoft YaHei",
		sans-serif;
}

.header {
	padding:20px 25px;
	background:#171a22;
	border-bottom:1px solid #292d38;
}

.header h1 {
	margin:0;
	font-size:24px;
}

.header p {
	margin:7px 0 0;
	color:#8e96a3;
}

.container {
	max-width:1100px;
	margin:25px auto;
	padding:0 18px;
}

.cards {
	display:grid;
	grid-template-columns:
		repeat(auto-fit,minmax(200px,1fr));
	gap:15px;
}

.card {
	background:#171a22;
	border:1px solid #292d38;
	border-radius:12px;
	padding:18px;
}

.card-title {
	color:#8e96a3;
	font-size:13px;
	margin-bottom:8px;
}

.card-value {
	font-size:20px;
	font-weight:600;
}

.online {
	color:#49d17d;
}

.offline {
	color:#ff5f5f;
}

.connecting {
	color:#f1c75b;
}

.panel {
	margin-top:20px;
	background:#171a22;
	border:1px solid #292d38;
	border-radius:12px;
	padding:20px;
}

.panel h2 {
	margin-top:0;
	font-size:18px;
}

.input {
	width:100%;
	margin-top:8px;
	margin-bottom:15px;
	padding:12px;
	background:#0f1117;
	color:#fff;
	border:1px solid #30363d;
	border-radius:8px;
	outline:none;
}

.input:focus {
	border-color:#58a6ff;
}

.buttons {
	margin-top:20px;
	display:flex;
	gap:10px;
	flex-wrap:wrap;
}

button {
	border:0;
	border-radius:8px;
	padding:11px 20px;
	font-size:14px;
	cursor:pointer;
}

.start {
	background:#2ea043;
	color:white;
}

.stop {
	background:#da3633;
	color:white;
}

.save {
	background:#238636;
	color:white;
}

.refresh {
	background:#30363d;
	color:white;
}

button:hover {
	opacity:.85;
}

.logs {
	margin-top:20px;
	background:#090b10;
	border:1px solid #292d38;
	border-radius:12px;
	padding:15px;
}

.logs-title {
	font-size:16px;
	margin-bottom:10px;
}

#log {
	height:430px;
	overflow-y:auto;
	font-family:Consolas,monospace;
	font-size:13px;
	white-space:pre-wrap;
	color:#c9d1d9;
}

.info {
	margin-top:20px;
	display:grid;
	grid-template-columns:
		repeat(auto-fit,minmax(280px,1fr));
	gap:15px;
}

.info div {
	background:#171a22;
	border:1px solid #292d38;
	border-radius:12px;
	padding:15px;
}

.label {
	color:#8e96a3;
	font-size:12px;
}

.value {
	margin-top:5px;
	word-break:break-all;
}

.tip {
	color:#8e96a3;
	font-size:13px;
	line-height:1.6;
}

</style>

</head>

<body>

<div class="header">

<h1>Qbot v0.2.1</h1>

<p>QQ Official Bot · WebSocket 管理后台</p>

</div>

<div class="container">

<div class="cards">

<div class="card">

<div class="card-title">
运行状态
</div>

<div id="running"
class="card-value">
停止
</div>

</div>

<div class="card">

<div class="card-title">
QQ Gateway
</div>

<div id="ready"
class="card-value">
未连接
</div>

</div>

<div class="card">

<div class="card-title">
Bot
</div>

<div id="bot"
class="card-value">
-
</div>

</div>

<div class="card">

<div class="card-title">
心跳
</div>

<div id="heartbeat"
class="card-value">
-
</div>

</div>

</div>

<div class="panel">

<h2>QQ Bot 配置</h2>

<div class="tip">
AppID 和 AppSecret 会保存到程序目录下的
qbot_config.json。
</div>

<label>AppID</label>

<input
	id="appID"
	class="input"
	type="text"
	placeholder="请输入 QQ Bot AppID"
>

<label>AppSecret</label>

<input
	id="appSecret"
	class="input"
	type="password"
	placeholder="请输入 QQ Bot AppSecret"
>

<div class="buttons">

<button
	class="save"
	onclick="saveConfig()"
>
保存配置
</button>

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

<div class="label">
AppID
</div>

<div id="appid"
class="value">
-
</div>

</div>

<div>

<div class="label">
Session ID
</div>

<div id="session"
class="value">
-
</div>

</div>

<div>

<div class="label">
Sequence
</div>

<div id="sequence"
class="value">
0
</div>

</div>

<div>

<div class="label">
重连次数
</div>

<div id="reconnect"
class="value">
0
</div>

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

async function loadConfig() {

	try {

		const data =
			await api("/api/config");

		document.getElementById("appID")
			.value =
			data.app_id || "";

	} catch(e) {

		console.error(e);

	}

}

async function saveConfig() {

	const appID =
		document.getElementById("appID").value.trim();

	const appSecret =
		document.getElementById("appSecret").value.trim();

	if (!appID) {

		alert("请输入 AppID");

		return;

	}

	if (!appSecret) {

		alert("请输入 AppSecret");

		return;

	}

	try {

		const data =
			await api(
				"/api/config",
				{
					method:"POST",

					headers:{
						"Content-Type":
							"application/json"
					},

					body:JSON.stringify({
						app_id:appID,
						app_secret:appSecret
					})
				}
			);

		if (data.ok) {

			alert(
				"配置保存成功"
			);

			document.getElementById("appSecret")
				.value = "";

			await refreshAll();

		} else {

			alert(
				"保存失败：" +
				(data.error || "未知错误")
			);

		}

	} catch(e) {

		alert(
			"请求失败：" + e
		);

	}

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
			data.sequence || 0;

		document.getElementById("heartbeat")
			.textContent =
			data.last_heartbeat || "-";

		document.getElementById("reconnect")
			.textContent =
			data.reconnect_count || 0;

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
			(data.logs || []).join("\n");

		log.scrollTop =
			log.scrollHeight;

	} catch(e) {

		console.error(e);

	}

}

async function startBot() {

	const data =
		await api(
			"/api/start",
			{
				method:"POST"
			}
		);

	await refreshAll();

}

async function stopBot() {

	const data =
		await api(
			"/api/stop",
			{
				method:"POST"
			}
		);

	await refreshAll();

}

async function refreshAll() {

	await refreshStatus();

	await refreshLogs();

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
// JSON
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

	fmt.Fprint(w, webHTML)
}

func writeJSON(
	w http.ResponseWriter,
	data interface{},
) {
	w.Header().Set(
		"Content-Type",
		"application/json; charset=utf-8",
	)

	_ = json.NewEncoder(w).Encode(data)
}

//
// ============================================================
// 辅助
// ============================================================
//

func sleepOrStop(duration time.Duration) {
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
// Ctrl+C
// ============================================================
//

func setupSignalHandler() {
	ch := make(chan os.Signal, 1)

	signal.Notify(
		ch,
		os.Interrupt,
		syscall.SIGTERM,
	)

	go func() {

		<-ch

		addLog("[SYSTEM] 正在退出")

		stopGateway()

		time.Sleep(300 * time.Millisecond)

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
	fmt.Println("======================================")
	fmt.Println()

	loadConfig()

	setupSignalHandler()

	go startWebServer()

	time.Sleep(300 * time.Millisecond)

	if appID != "" && appSecret != "" {
		startGateway()
	} else {
		addLog(
			"[SYSTEM] 请打开 Web 管理后台配置 AppID / AppSecret",
		)
	}

	fmt.Println()
	fmt.Println("Web 管理后台：")
	fmt.Println("http://127.0.0.1:8080")
	fmt.Println()

	select {}
}
