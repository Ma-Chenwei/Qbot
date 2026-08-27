package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)


// ============================================================
// Runtime
// ============================================================

var Runtime = struct {
	sync.RWMutex

	Running bool

	StartTime time.Time

	MessageCount int64

	SentMessageCount int64

	AIMessageCount int64

	LastError string
}{}

// ============================================================
// QbotContext
// ============================================================

var QbotContext = struct {
	sync.RWMutex

	QQAccount string

	QQNickname string

	QQStatus string

	QQConnected bool

	LastGroupID string

	LastUserID string

	LastMessage string

	LastReply string
}{}

// ============================================================
// QQ Message
// ============================================================

type QQMessage struct {
	ID string `json:"id"`

	Type string `json:"type"`

	MessageType string `json:"message_type"`

	Message string `json:"message"`

	UserID string `json:"user_id"`

	GroupID string `json:"group_id"`

	Nickname string `json:"nickname"`

	Timestamp int64 `json:"timestamp"`

	Raw any `json:"raw,omitempty"`
}

// ============================================================
// QQ Protocol Adapter
// ============================================================

type QQProtocolAdapter interface {

	Login() error

	Logout() error

	Connect() error

	Disconnect() error

	IsConnected() bool

	SendPrivateMessage(
		userID string,
		message string,
	) error

	SendGroupMessage(
		groupID string,
		message string,
	) error

	OnMessage(
		handler func(QQMessage),
	)
}

// ============================================================
// Qbot QQ Client
//
// 这是 Qbot 自己的 QQ Client 层。
// ============================================================

type QbotQQClient struct {

	mu sync.RWMutex

	account string

	nickname string

	connected bool

	loggedIn bool

	handler func(QQMessage)

	stopChan chan struct{}

	started bool
}

// ============================================================
// NewQbotQQClient
// ============================================================

func NewQbotQQClient(
	account string,
) *QbotQQClient {

	return &QbotQQClient{

		account: account,

		stopChan: make(chan struct{}),
	}
}

// ============================================================
// Login
//
// 这里不实现绕过 QQ 官方验证的登录协议。
// ============================================================

func (c *QbotQQClient) Login() error {

	c.mu.Lock()

	defer c.mu.Unlock()

	if c.loggedIn {
		return nil
	}

	account := strings.TrimSpace(
    	QbotConfig.QQ.Account,
	)

	if account == "" {
    	return errors.New(
        	"QQ账号为空，请在 WebUI 设置中填写 QQ 账号并保存",
    	)
	}

	c.account = account

	log.Println(
		"[QQ] QQ Client 登录初始化:",
		c.account,
	)

	// --------------------------------------------------------
	// 当前版本：
	//
	// 这里只建立 Qbot Client Session 状态。
	//
	// 实际 QQ 登录需要后续接入合法/授权的 QQ 登录接口。
	// --------------------------------------------------------

	c.loggedIn = true

	return nil
}

// ============================================================
// Connect
// ============================================================

func (c *QbotQQClient) Connect() error {

	c.mu.Lock()

	defer c.mu.Unlock()

	if !c.loggedIn {

		return errors.New(
			"QQ 尚未登录",
		)
	}

	if c.connected {
		return nil
	}

	log.Println(
		"[QQ] QQ Client 正在建立连接...",
	)

	c.connected = true

	if !c.started {

		c.started = true

		go c.run()
	}

	return nil
}

// ============================================================
// Disconnect
// ============================================================

func (c *QbotQQClient) Disconnect() error {

	c.mu.Lock()

	if !c.connected {

		c.mu.Unlock()

		return nil
	}

	c.connected = false

	c.mu.Unlock()

	log.Println(
		"[QQ] QQ Client 已断开",
	)

	return nil
}

// ============================================================
// Logout
// ============================================================

func (c *QbotQQClient) Logout() error {

	c.mu.Lock()

	c.loggedIn = false

	c.connected = false

	c.mu.Unlock()

	log.Println(
		"[QQ] QQ Client 已退出登录",
	)

	return nil
}

// ============================================================
// IsConnected
// ============================================================

func (c *QbotQQClient) IsConnected() bool {

	c.mu.RLock()

	value := c.connected

	c.mu.RUnlock()

	return value
}

// ============================================================
// SendPrivateMessage
// ============================================================

func (c *QbotQQClient) SendPrivateMessage(
	userID string,
	message string,
) error {

	c.mu.RLock()

	connected := c.connected

	c.mu.RUnlock()

	if !connected {

		return errors.New(
			"QQ Client 未连接",
		)
	}

	userID =
		strings.TrimSpace(userID)

	message =
		strings.TrimSpace(message)

	if userID == "" {

		return errors.New(
			"QQ用户ID为空",
		)
	}

	if message == "" {

		return errors.New(
			"消息为空",
		)
	}

	// --------------------------------------------------------
	// 实际发送接口在 QQ Transport 层实现。
	// --------------------------------------------------------

	log.Printf(
		"[QQ] PrivateMessage -> %s: %s",
		userID,
		message,
	)

	return nil
}

// ============================================================
// SendGroupMessage
// ============================================================

func (c *QbotQQClient) SendGroupMessage(
	groupID string,
	message string,
) error {

	c.mu.RLock()

	connected := c.connected

	c.mu.RUnlock()

	if !connected {

		return errors.New(
			"QQ Client 未连接",
		)
	}

	groupID =
		strings.TrimSpace(groupID)

	message =
		strings.TrimSpace(message)

	if groupID == "" {

		return errors.New(
			"QQ群ID为空",
		)
	}

	if message == "" {

		return errors.New(
			"消息为空",
		)
	}

	log.Printf(
		"[QQ] GroupMessage -> %s: %s",
		groupID,
		message,
	)

	return nil
}

// ============================================================
// OnMessage
// ============================================================

func (c *QbotQQClient) OnMessage(
	handler func(QQMessage),
) {

	c.mu.Lock()

	c.handler = handler

	c.mu.Unlock()
}

// ============================================================
// run
// ============================================================

func (c *QbotQQClient) run() {

	ticker := time.NewTicker(
		10 * time.Second,
	)

	defer ticker.Stop()

	for {

		select {

		case <-ticker.C:

			c.mu.RLock()

			connected := c.connected

			c.mu.RUnlock()

			if !connected {
				continue
			}

			// ------------------------------------------------
			// QQ Transport 心跳位置
			// ------------------------------------------------
			//
			// 后续实际 QQ Transport 接入以后，
			// 在这里进行心跳/事件读取。
			//

		case <-c.stopChan:

			return
		}
	}
}

// ============================================================
// QQ Adapter
// ============================================================

var qqAdapterMu sync.RWMutex

var qqAdapter QQProtocolAdapter

// ============================================================
// SetQQAdapter
// ============================================================

func SetQQAdapter(
	adapter QQProtocolAdapter,
) {

	qqAdapterMu.Lock()

	qqAdapter = adapter

	qqAdapterMu.Unlock()
}

// ============================================================
// GetQQAdapter
// ============================================================

func GetQQAdapter() QQProtocolAdapter {

	qqAdapterMu.RLock()

	adapter := qqAdapter

	qqAdapterMu.RUnlock()

	return adapter
}

// ============================================================
// main
// ============================================================

func main() {

	StartupBanner()

	webOnly := ParseQbotFlags()

	if webOnly {

		if err := LoadQbotConfig(); err != nil {
			log.Fatal(err)
		}

		if err := StartWeb(); err != nil {
			log.Fatal(err)
		}

		select {}
	}

	// --------------------------------------------------------
	// Runtime
	// --------------------------------------------------------

	Runtime.Lock()

	Runtime.Running = true

	Runtime.StartTime = time.Now()

	Runtime.MessageCount = 0

	Runtime.SentMessageCount = 0

	Runtime.AIMessageCount = 0

	Runtime.LastError = ""

	Runtime.Unlock()

	// --------------------------------------------------------
	// Context
	// --------------------------------------------------------

	ctx, cancel := context.WithCancel(
		context.Background(),
	)

	defer cancel()

	// --------------------------------------------------------
	// Signal
	// --------------------------------------------------------

	signalChan :=
		make(chan os.Signal, 1)

	signal.Notify(
		signalChan,
		os.Interrupt,
		syscall.SIGTERM,
	)

	// --------------------------------------------------------
	// 初始化
	// --------------------------------------------------------

	log.Println(
		"[Qbot] 正在初始化...",
	)

	if err := InitQbot(); err != nil {

		SetLastError(err)

		log.Fatal(
			"[Qbot] 初始化失败:",
			err,
		)
	}

	log.Println(
		"[Qbot] 初始化完成",
	)

	// --------------------------------------------------------
	// 初始化 QQ Client
	// --------------------------------------------------------

	cfg := GetConfig()

	if cfg.QQ.Enabled {

		log.Println(
			"[QQ] 正在初始化 QQ Client...",
		)

		client :=
			NewQbotQQClient(
				cfg.QQ.Account,
			)

		SetQQAdapter(client)

		log.Println(
			"[QQ] QQ Client 已创建",
		)
	}

	// --------------------------------------------------------
	// AI
	// --------------------------------------------------------

	if cfg.AI.Enabled {

		log.Println(
			"[AI] 正在初始化...",
		)

		if err := ReloadAI(); err != nil {

			SetLastError(err)

			log.Println(
				"[AI] 初始化失败:",
				err,
			)

		} else {

			log.Println(
				"[AI] 初始化完成",
			)
		}

	} else {

		log.Println(
			"[AI] 当前未启用",
		)
	}

	// --------------------------------------------------------
	// Web
	// --------------------------------------------------------

	if cfg.Web.Enabled {

		if err := StartWeb(); err != nil {

			SetLastError(err)

			log.Println(
				"[Web] 启动失败:",
				err,
			)

		} else {

			log.Println(
				"[Web] WebUI 已启动",
			)
		}
	}

	// --------------------------------------------------------
	// QQ
	// --------------------------------------------------------

	if cfg.QQ.Enabled {

		log.Println(
			"[QQ] 正在启动...",
		)

		go func() {

			if err := StartQQ(); err != nil {

				SetLastError(err)

				log.Println(
					"[QQ] 启动失败:",
					err,
				)
			}

		}()

	} else {

		log.Println(
			"[QQ] 当前未启用",
		)
	}

	// --------------------------------------------------------
	// Runtime
	// --------------------------------------------------------

	go runtimeLoop(ctx)

	// --------------------------------------------------------
	// 等待退出
	// --------------------------------------------------------

	sig := <-signalChan

	log.Println()

	log.Println(
		"[Qbot] 收到退出信号:",
		sig,
	)

	cancel()

	shutdownQbot()

	log.Println(
		"[Qbot] 已退出",
	)
}

// ============================================================
// InitQbot
// ============================================================

func InitQbot() error {

	if err := LoadQbotConfig(); err != nil {
		return err
	}

	cfg := GetConfig()

	if cfg.Memory.Enabled {

		if err := InitMemory(); err != nil {
			return err
		}

		log.Println(
			"[Memory] 已初始化",
		)

	} else {

		log.Println(
			"[Memory] 当前未启用",
		)
	}

	log.Println(
		"[Game] 游戏模块准备完成",
	)

	log.Println(
		"[Message] 消息处理器准备完成",
	)

	return nil
}

// ============================================================
// Runtime Loop
// ============================================================

func runtimeLoop(
	ctx context.Context,
) {

	ticker :=
		time.NewTicker(
			5 * time.Second,
		)

	defer ticker.Stop()

	for {

		select {

		case <-ctx.Done():

			return

		case <-ticker.C:

			runtimeTick()
		}
	}
}

// ============================================================
// runtimeTick
// ============================================================

func runtimeTick() {

	Runtime.RLock()

	running :=
		Runtime.Running

	Runtime.RUnlock()

	if !running {
		return
	}

	cfg :=
		GetConfig()

	if !cfg.QQ.Enabled {
		return
	}

	if !cfg.QQ.AutoReconnect {
		return
	}

	if IsQQConnected() {
		return
	}

	go func() {

		if err := EnsureQQConnection(); err != nil {

			SetLastError(err)

		}

	}()
}

// ============================================================
// StartQQ
// ============================================================

func StartQQ() error {

	adapter :=
		GetQQAdapter()

	if adapter == nil {

		return errors.New(
			"QQ Client 未初始化",
		)
	}

	account :=
		GetConfig().QQ.Account

	SetQQStatus(
		account,
		"",
		"logging_in",
		false,
	)

	log.Println(
		"[QQ] 正在登录...",
	)

	if err := adapter.Login(); err != nil {

		SetQQStatus(
			account,
			"",
			"login_failed",
			false,
		)

		return err
	}

	SetQQStatus(
		account,
		"",
		"connecting",
		false,
	)

	if err := adapter.Connect(); err != nil {

		_ = adapter.Logout()

		SetQQStatus(
			account,
			"",
			"connection_failed",
			false,
		)

		return err
	}

	adapter.OnMessage(
		HandleQQMessage,
	)

	SetQQStatus(
		account,
		"",
		"online",
		true,
	)

	log.Println(
		"[QQ] QQ Client 已在线",
	)

	return nil
}

// ============================================================
// EnsureQQConnection
// ============================================================

func EnsureQQConnection() error {

	if IsQQConnected() {
		return nil
	}

	adapter :=
		GetQQAdapter()

	if adapter == nil {

		return errors.New(
			"QQ Client 未初始化",
		)
	}

	return StartQQ()
}

// ============================================================
// StopQQ
// ============================================================

func StopQQ() {

	adapter :=
		GetQQAdapter()

	if adapter == nil {

		SetQQStatus(
			"",
			"",
			"offline",
			false,
		)

		return
	}

	log.Println(
		"[QQ] 正在断开...",
	)

	if err := adapter.Disconnect();
		err != nil {

		log.Println(
			"[QQ] Disconnect:",
			err,
		)
	}

	if err := adapter.Logout();
		err != nil {

		log.Println(
			"[QQ] Logout:",
			err,
		)
	}

	QbotContext.RLock()

	account :=
		QbotContext.QQAccount

	nickname :=
		QbotContext.QQNickname

	QbotContext.RUnlock()

	SetQQStatus(
		account,
		nickname,
		"offline",
		false,
	)
}

// ============================================================
// SendPrivateMessage
// ============================================================

func SendPrivateMessage(
	userID string,
	message string,
) error {

	adapter :=
		GetQQAdapter()

	if adapter == nil {
		return errors.New(
			"QQ Client 未初始化",
		)
	}

	if !adapter.IsConnected() {
		return errors.New(
			"QQ Client 未连接",
		)
	}

	message =
		strings.TrimSpace(message)

	if message == "" {
		return errors.New(
			"消息不能为空",
		)
	}

	if err :=
		adapter.SendPrivateMessage(
			userID,
			message,
		); err != nil {

		return err
	}

	SetLastReply(message)

	return nil
}

// ============================================================
// SendGroupMessage
// ============================================================

func SendGroupMessage(
	groupID string,
	message string,
) error {

	adapter :=
		GetQQAdapter()

	if adapter == nil {
		return errors.New(
			"QQ Client 未初始化",
		)
	}

	if !adapter.IsConnected() {
		return errors.New(
			"QQ Client 未连接",
		)
	}

	message =
		strings.TrimSpace(message)

	if message == "" {
		return errors.New(
			"消息不能为空",
		)
	}

	if err :=
		adapter.SendGroupMessage(
			groupID,
			message,
		); err != nil {

		return err
	}

	SetLastReply(message)

	return nil
}

// ============================================================
// HandleQQMessage
// ============================================================

func HandleQQMessage(
	msg QQMessage,
) {

	SetLastMessage(
		msg.GroupID,
		msg.UserID,
		msg.Message,
	)

	if msg.MessageType == "private" ||
		msg.Type == "private" {

		handlePrivateMessage(msg)

		return
	}

	if msg.MessageType == "group" ||
		msg.Type == "group" {

		handleGroupMessage(msg)

		return
	}
}

// ============================================================
// handlePrivateMessage
// ============================================================

func handlePrivateMessage(
	msg QQMessage,
) {

	message :=
		strings.TrimSpace(
			msg.Message,
		)

	if message == "" {
		return
	}

	cfg :=
		GetConfig()

	if !cfg.AI.Enabled {
		return
	}

	reply, err :=
		AskAI(
			msg.UserID,
			"",
			message,
		)

	if err != nil {

		SetLastError(err)

		return
	}

	if reply == "" {
		return
	}

	AddAIMessage()

	if err :=
		SendPrivateMessage(
			msg.UserID,
			reply,
		); err != nil {

		SetLastError(err)
	}
}

// ============================================================
// handleGroupMessage
// ============================================================

func handleGroupMessage(
	msg QQMessage,
) {

	message :=
		strings.TrimSpace(
			msg.Message,
		)

	if message == "" {
		return
	}

	cfg :=
		GetConfig()

	if strings.HasPrefix(
		message,
		"/menu",
	) {

		if err :=
			SendGroupMessage(
				msg.GroupID,
				buildMenuText(),
			); err != nil {

			SetLastError(err)
		}

		return
	}

	if !cfg.AI.Enabled {
		return
	}

	if !cfg.GroupAI.Enabled {
		return
	}

	mentioned :=
		messageContainsBotMention(
			message,
		)

	if !ShouldGroupAIReply(
		message,
		mentioned,
	) {
		return
	}

	reply, err :=
		GroupAIReply(
			context.Background(),
			msg.UserID,
			msg.GroupID,
			msg.Nickname,
			message,
		)

	if err != nil {

		SetLastError(err)

		return
	}

	if reply == "" {
		return
	}

	AddAIMessage()

	if err :=
		SendGroupMessage(
			msg.GroupID,
			reply,
		); err != nil {

		SetLastError(err)
	}
}

// ============================================================
// buildMenuText
// ============================================================

func buildMenuText() string {

	return `Qbot

/menu        查看菜单
/ai 内容     AI 对话
/天气 城市   查询天气
/游戏 名字   开始游戏

AI、Memory、游戏、群自动回复等功能
可以在 Qbot WebUI 中配置。`
}

// ============================================================
// Mention
// ============================================================

func messageContainsBotMention(
	message string,
) bool {

	_ = message

	return false
}

// ============================================================
// Shutdown
// ============================================================

func shutdownQbot() {

	Runtime.Lock()

	Runtime.Running = false

	Runtime.Unlock()

	log.Println(
		"[Qbot] 正在停止 QQ...",
	)

	StopQQ()

	log.Println(
		"[Qbot] 正在停止 WebUI...",
	)

	StopWeb()

	log.Println(
		"[Qbot] 正在保存状态...",
	)

	SaveRuntimeState()

	log.Println(
		"[Qbot] Shutdown 完成",
	)
}

// ============================================================
// RuntimeSnapshot
// ============================================================

type RuntimeSnapshot struct {

	Running bool `json:"running"`

	StartTime time.Time `json:"start_time"`

	Uptime string `json:"uptime"`

	MessageCount int64 `json:"message_count"`

	SentMessageCount int64 `json:"sent_message_count"`

	AIMessageCount int64 `json:"ai_message_count"`

	QQConnected bool `json:"qq_connected"`

	AIReady bool `json:"ai_ready"`

	LastError string `json:"last_error"`
}

// ============================================================
// GetRuntimeSnapshot
// ============================================================

func GetRuntimeSnapshot() RuntimeSnapshot {

	Runtime.RLock()

	running :=
		Runtime.Running

	startTime :=
		Runtime.StartTime

	messageCount :=
		Runtime.MessageCount

	sentMessageCount :=
		Runtime.SentMessageCount

	aiMessageCount :=
		Runtime.AIMessageCount

	lastError :=
		Runtime.LastError

	Runtime.RUnlock()

	QbotContext.RLock()

	qqConnected :=
		QbotContext.QQConnected

	QbotContext.RUnlock()

	uptime :=
		"0s"

	if !startTime.IsZero() {

		uptime =
			time.Since(
				startTime,
			).
				Round(
					time.Second,
				).
				String()
	}

	return RuntimeSnapshot{

		Running:
			running,

		StartTime:
			startTime,

		Uptime:
			uptime,

		MessageCount:
			messageCount,

		SentMessageCount:
			sentMessageCount,

		AIMessageCount:
			aiMessageCount,

		QQConnected:
			qqConnected,

		AIReady:
			IsAIReady(),

		LastError:
			lastError,
	}
}

// ============================================================
// Statistics
// ============================================================

func AddReceivedMessage() {

	Runtime.Lock()

	Runtime.MessageCount++

	Runtime.Unlock()
}

func AddSentMessage() {

	Runtime.Lock()

	Runtime.SentMessageCount++

	Runtime.Unlock()
}

func AddAIMessage() {

	Runtime.Lock()

	Runtime.AIMessageCount++

	Runtime.Unlock()
}

// ============================================================
// Error
// ============================================================

func SetLastError(
	err error,
) {

	if err == nil {
		return
	}

	Runtime.Lock()

	Runtime.LastError =
		err.Error()

	Runtime.Unlock()
}

// ============================================================
// Web State
// ============================================================

var webRunning bool

var webRunningMu sync.RWMutex

func SetWebRunning(
	value bool,
) {

	webRunningMu.Lock()

	webRunning =
		value

	webRunningMu.Unlock()
}

func IsWebRunning() bool {

	webRunningMu.RLock()

	value :=
		webRunning

	webRunningMu.RUnlock()

	return value
}

// ============================================================
// QQ State
// ============================================================

func SetQQStatus(
	account string,
	nickname string,
	status string,
	connected bool,
) {

	QbotContext.Lock()

	QbotContext.QQAccount =
		account

	QbotContext.QQNickname =
		nickname

	QbotContext.QQStatus =
		status

	QbotContext.QQConnected =
		connected

	QbotContext.Unlock()
}

// ============================================================
// IsQQConnected
// ============================================================

func IsQQConnected() bool {

	QbotContext.RLock()

	value :=
		QbotContext.QQConnected

	QbotContext.RUnlock()

	return value
}

// ============================================================
// Message Context
// ============================================================

func SetLastMessage(
	groupID string,
	userID string,
	message string,
) {

	QbotContext.Lock()

	QbotContext.LastGroupID =
		groupID

	QbotContext.LastUserID =
		userID

	QbotContext.LastMessage =
		message

	QbotContext.Unlock()

	AddReceivedMessage()
}

func SetLastReply(
	reply string,
) {

	QbotContext.Lock()

	QbotContext.LastReply =
		reply

	QbotContext.Unlock()

	AddSentMessage()
}

// ============================================================
// SaveRuntimeState
// ============================================================

func SaveRuntimeState() {

	log.Println(
		"[Qbot] Runtime 状态已保存",
	)
}

// ============================================================
// End of main.go
// ============================================================