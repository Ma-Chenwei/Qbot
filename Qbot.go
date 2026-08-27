package main

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	mathrand "math/rand"

	"github.com/gin-gonic/gin"
	"github.com/go-resty/resty/v2"
	"github.com/robfig/cron/v3"
	"github.com/sirupsen/logrus"

	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/event"
	"github.com/tencent-connect/botgo/interaction/webhook"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/token"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	QbotName    = "Qbot"
	QbotVersion = "1.0.0"
)

var (
	db     *gorm.DB
	logger = logrus.New()

	configMu sync.RWMutex

	rateMu    sync.Mutex
	lastSpeak = make(map[string]time.Time)

	guessMu sync.Mutex
	guesses = make(map[string]int)

	botMu      sync.RWMutex
	botAPI     openapi.OpenAPI
	botCancel  context.CancelFunc
	botRunning bool
	botAppID   string
	botSecret  string

	botInitMu sync.Mutex
)

type MessageRecord struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	GroupID   string    `json:"group_id"`
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	Content   string    `json:"content"`
	IsCommand bool      `json:"is_command"`
	Command   string    `json:"command"`
	CreatedAt time.Time `json:"created_at"`
}

type SensitiveWord struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Word string `gorm:"uniqueIndex;not null" json:"word"`
}

type BotConfig struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	ConfigKey   string `gorm:"uniqueIndex;not null" json:"key"`
	ConfigValue string `json:"value"`
}

type CheckinRecord struct {
	ID        uint      `gorm:"primaryKey"`
	GroupID   string    `json:"group_id"`
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	Date      string    `json:"date"`
	CreatedAt time.Time `json:"created_at"`
}

var defaultConfigs = map[string]string{
	"qq_enabled": "false",
	"qq_appid":   "",
	"qq_secret":  "",

	"weather_enabled": "false",
	"weather_api_key": "",

	"sensitive_enabled": "true",

	"rate_limit_enabled": "true",
	"rate_limit_seconds": "5",

	"command_enabled": "true",
	"command_prefix":  "/",

	"checkin_enabled": "true",

	"morning_enabled": "false",
	"morning_time":    "08:00",
	"morning_message": "早上好！",

	"bot_reply_enabled": "true",

	"port": "8080",

	"debug_enabled": "false",
}

type QQMessage struct {
	ID       string
	GroupID  string
	UserID   string
	Username string
	Content  string
}

func getConfig(key string) string {
	configMu.RLock()
	defer configMu.RUnlock()

	var cfg BotConfig

	err := db.Where("config_key = ?", key).First(&cfg).Error
	if err != nil {
		return defaultConfigs[key]
	}

	return cfg.ConfigValue
}

func setConfig(key, value string) error {
	configMu.Lock()
	defer configMu.Unlock()

	var cfg BotConfig

	err := db.Where("config_key = ?", key).First(&cfg).Error

	if err == gorm.ErrRecordNotFound {
		return db.Create(&BotConfig{
			ConfigKey:   key,
			ConfigValue: value,
		}).Error
	}

	if err != nil {
		return err
	}

	cfg.ConfigValue = value
	return db.Save(&cfg).Error
}

func initDefaultConfigs() error {
	for key, value := range defaultConfigs {
		var count int64

		if err := db.Model(&BotConfig{}).
			Where("config_key = ?", key).
			Count(&count).Error; err != nil {
			return err
		}

		if count == 0 {
			if err := db.Create(&BotConfig{
				ConfigKey:   key,
				ConfigValue: value,
			}).Error; err != nil {
				return err
			}
		}
	}

	return nil
}

func initDatabase() error {
	var err error

	db, err = gorm.Open(
		sqlite.Open("qbot.db"),
		&gorm.Config{},
	)
	if err != nil {
		return err
	}

	err = db.AutoMigrate(
		&MessageRecord{},
		&SensitiveWord{},
		&BotConfig{},
		&CheckinRecord{},
	)
	if err != nil {
		return err
	}

	if err := initDefaultConfigs(); err != nil {
		return err
	}

	logger.Info("SQLite 初始化成功")
	return nil
}

func registerQQHandlers() {
	event.RegisterHandlers(
		func(evt *dto.WSPayload, data *dto.WSGroupATMessageData) error {
			return handleOfficialGroupMessage(data)
		},
	)
}

func handleOfficialGroupMessage(data *dto.WSGroupATMessageData) error {
	if data == nil {
		return nil
	}

	if getConfig("qq_enabled") != "true" {
		return nil
	}

	userID := ""
	username := ""

	if data.Author != nil {
		userID = data.Author.ID
		username = data.Author.Username
	}

	if username == "" && data.Member != nil {
		username = data.Member.Nick
	}

	msg := QQMessage{
		ID:       data.ID,
		GroupID:  data.GroupID,
		UserID:   userID,
		Username: username,
		Content:  strings.TrimSpace(data.Content),
	}

	go func() {
		if err := processGroupMessage(msg); err != nil {
			logger.Errorf("消息处理失败: %v", err)
		}
	}()

	return nil
}

func startQQBot() error {
	botInitMu.Lock()
	defer botInitMu.Unlock()

	appID := strings.TrimSpace(getConfig("qq_appid"))
	secret := strings.TrimSpace(getConfig("qq_secret"))

	if appID == "" {
		return fmt.Errorf("QQ AppID 未设置")
	}

	if secret == "" {
		return fmt.Errorf("QQ AppSecret 未设置")
	}

	botMu.RLock()
	same := botRunning &&
		botAppID == appID &&
		botSecret == secret
	botMu.RUnlock()

	if same {
		return nil
	}

	stopQQBotLocked()

	credentials := &token.QQBotCredentials{
		AppID:     appID,
		AppSecret: secret,
	}

	ctx, cancel := context.WithCancel(context.Background())

	tokenSource := token.NewQQBotTokenSource(credentials)

	if err := token.StartRefreshAccessToken(
		ctx,
		tokenSource,
	); err != nil {
		cancel()
		return fmt.Errorf("QQ AccessToken 初始化失败: %w", err)
	}

	api := botgo.NewOpenAPI(
		appID,
		tokenSource,
	).
		WithTimeout(10 * time.Second).
		SetDebug(getConfig("debug_enabled") == "true")

	botMu.Lock()
	botAPI = api
	botCancel = cancel
	botRunning = true
	botAppID = appID
	botSecret = secret
	botMu.Unlock()

	logger.Infof("QQ Bot 初始化成功，AppID=%s", appID)

	return nil
}

func stopQQBot() {
	botInitMu.Lock()
	defer botInitMu.Unlock()

	stopQQBotLocked()
}

func stopQQBotLocked() {
	botMu.Lock()
	defer botMu.Unlock()

	if botCancel != nil {
		botCancel()
	}

	botCancel = nil
	botAPI = nil
	botRunning = false
	botAppID = ""
	botSecret = ""
}

func syncQQBot() {
	if getConfig("qq_enabled") != "true" {
		stopQQBot()
		return
	}

	if err := startQQBot(); err != nil {
		logger.Warnf("QQ Bot 启动失败: %v", err)
	}
}

func qqWebhookHandler(c *gin.Context) {
	appID := strings.TrimSpace(getConfig("qq_appid"))
	secret := strings.TrimSpace(getConfig("qq_secret"))

	if appID == "" || secret == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "QQ Bot 尚未配置 AppID / AppSecret",
		})
		return
	}

	if getConfig("qq_enabled") != "true" {
		c.JSON(http.StatusOK, gin.H{
			"ok":      true,
			"enabled": false,
		})
		return
	}

	credentials := &token.QQBotCredentials{
		AppID:     appID,
		AppSecret: secret,
	}

	webhook.HTTPHandler(
		c.Writer,
		c.Request,
		credentials,
	)
}

func sendQQMessage(groupID, content, replyMessageID string) error {
	if getConfig("qq_enabled") != "true" {
		return nil
	}

	if groupID == "" {
		return fmt.Errorf("groupID 为空")
	}

	if content == "" {
		return nil
	}

	botMu.RLock()
	api := botAPI
	running := botRunning
	botMu.RUnlock()

	if !running || api == nil {
		return fmt.Errorf("QQ Bot 尚未运行")
	}

	msg := &dto.MessageToCreate{
		Content: content,
		MsgType: dto.TextMsg,
		MsgID:   replyMessageID,
	}

	_, err := api.PostGroupMessage(
		context.Background(),
		groupID,
		msg,
	)

	if err != nil {
		logger.Errorf("QQ 群消息发送失败: %v", err)
		return err
	}

	return nil
}

func processGroupMessage(msg QQMessage) error {
	content := strings.TrimSpace(msg.Content)

	if content == "" {
		return nil
	}

	prefix := getConfig("command_prefix")
	if prefix == "" {
		prefix = "/"
	}

	isCommand := strings.HasPrefix(content, prefix)
	command := ""

	if isCommand {
		tmp := strings.TrimSpace(
			strings.TrimPrefix(content, prefix),
		)

		fields := strings.Fields(tmp)

		if len(fields) > 0 {
			command = strings.ToLower(fields[0])
		}
	}

	record := MessageRecord{
		GroupID:   msg.GroupID,
		UserID:    msg.UserID,
		Username:  msg.Username,
		Content:   content,
		IsCommand: isCommand,
		Command:   command,
		CreatedAt: time.Now(),
	}

	if err := db.Create(&record).Error; err != nil {
		logger.Warnf("消息保存失败: %v", err)
	}

	if checkRateLimit(msg.UserID) {
		return nil
	}

	if hit, word := checkSensitive(content); hit {
		logger.Infof(
			"敏感词命中 group=%s user=%s word=%s",
			msg.GroupID,
			msg.UserID,
			word,
		)

		if getConfig("bot_reply_enabled") == "true" {
			_ = sendQQMessage(
				msg.GroupID,
				"⚠️ 请注意文明交流，消息包含敏感词。",
				msg.ID,
			)
		}

		return nil
	}

	if !isCommand {
		return nil
	}

	if getConfig("command_enabled") != "true" {
		return nil
	}

	withoutPrefix := strings.TrimSpace(
		strings.TrimPrefix(content, prefix),
	)

	fields := strings.Fields(withoutPrefix)

	if len(fields) == 0 {
		return nil
	}

	command = strings.ToLower(fields[0])
	args := strings.TrimSpace(
		strings.TrimPrefix(
			withoutPrefix,
			fields[0],
		),
	)

	return handleCommand(msg, command, args)
}

func checkRateLimit(userID string) bool {
	if getConfig("rate_limit_enabled") != "true" {
		return false
	}

	if userID == "" {
		return false
	}

	seconds, err := strconv.Atoi(
		getConfig("rate_limit_seconds"),
	)

	if err != nil || seconds <= 0 {
		seconds = 5
	}

	now := time.Now()

	rateMu.Lock()
	defer rateMu.Unlock()

	last, exists := lastSpeak[userID]

	if exists &&
		now.Sub(last) < time.Duration(seconds)*time.Second {
		return true
	}

	lastSpeak[userID] = now

	if len(lastSpeak) > 10000 {
		for id, t := range lastSpeak {
			if now.Sub(t) > 10*time.Minute {
				delete(lastSpeak, id)
			}
		}
	}

	return false
}

func checkSensitive(content string) (bool, string) {
	if getConfig("sensitive_enabled") != "true" {
		return false, ""
	}

	var words []SensitiveWord

	if err := db.Find(&words).Error; err != nil {
		return false, ""
	}

	lower := strings.ToLower(content)

	for _, word := range words {
		if word.Word == "" {
			continue
		}

		if strings.Contains(
			lower,
			strings.ToLower(word.Word),
		) {
			return true, word.Word
		}
	}

	return false, ""
}

func handleCommand(msg QQMessage, command, args string) error {
	switch command {
	case "help":
		recordCommand(command)

		return sendQQMessage(
			msg.GroupID,
			"Qbot 指令：\n"+
				"/help - 查看帮助\n"+
				"/dice - 掷骰子\n"+
				"/guess N - 猜数字\n"+
				"/weather 城市 - 查询天气\n"+
				"/checkin - 签到\n"+
				"/status - 查看状态",
			msg.ID,
		)

	case "dice":
		recordCommand(command)

		n := mathrand.Intn(100) + 1

		return sendQQMessage(
			msg.GroupID,
			fmt.Sprintf("🎲 你掷出了：%d", n),
			msg.ID,
		)

	case "guess":
		recordCommand(command)
		return handleGuess(msg, args)

	case "weather":
		recordCommand(command)

		if getConfig("weather_enabled") != "true" {
			return sendQQMessage(
				msg.GroupID,
				"天气功能目前没有开启。",
				msg.ID,
			)
		}

		if args == "" {
			return sendQQMessage(
				msg.GroupID,
				"用法：/weather 城市",
				msg.ID,
			)
		}

		go handleWeather(
			msg.GroupID,
			args,
			msg.ID,
		)

		return nil

	case "checkin":
		recordCommand(command)

		if getConfig("checkin_enabled") != "true" {
			return sendQQMessage(
				msg.GroupID,
				"签到功能目前没有开启。",
				msg.ID,
			)
		}

		return handleCheckin(msg)

	case "status":
		recordCommand(command)

		botMu.RLock()
		running := botRunning
		botMu.RUnlock()

		text := fmt.Sprintf(
			"Qbot %s\nQQ Bot：%v\n天气：%v\n敏感词：%v\n防刷屏：%v",
			QbotVersion,
			running,
			getConfig("weather_enabled") == "true",
			getConfig("sensitive_enabled") == "true",
			getConfig("rate_limit_enabled") == "true",
		)

		return sendQQMessage(
			msg.GroupID,
			text,
			msg.ID,
		)

	default:
		return sendQQMessage(
			msg.GroupID,
			"未知指令，发送 /help 查看帮助。",
			msg.ID,
		)
	}
}

func handleGuess(msg QQMessage, args string) error {
	if args == "" {
		return sendQQMessage(
			msg.GroupID,
			"用法：/guess 50",
			msg.ID,
		)
	}

	fields := strings.Fields(args)

	number, err := strconv.Atoi(fields[0])

	if err != nil || number < 1 || number > 100 {
		return sendQQMessage(
			msg.GroupID,
			"请输入 1~100 的数字。",
			msg.ID,
		)
	}

	key := msg.GroupID

	guessMu.Lock()

	target, exists := guesses[key]

	if !exists {
		target = mathrand.Intn(100) + 1
		guesses[key] = target
	}

	guessMu.Unlock()

	if number == target {
		guessMu.Lock()
		guesses[key] = mathrand.Intn(100) + 1
		guessMu.Unlock()

		return sendQQMessage(
			msg.GroupID,
			fmt.Sprintf(
				"🎉 猜对了！答案就是 %d，新的一轮已经开始。",
				target,
			),
			msg.ID,
		)
	}

	if number < target {
		return sendQQMessage(
			msg.GroupID,
			"太小了。",
			msg.ID,
		)
	}

	return sendQQMessage(
		msg.GroupID,
		"太大了。",
		msg.ID,
	)
}

func handleCheckin(msg QQMessage) error {
	today := time.Now().Format("2006-01-02")

	var existing CheckinRecord

	err := db.
		Where(
			"group_id = ? AND user_id = ? AND date = ?",
			msg.GroupID,
			msg.UserID,
			today,
		).
		First(&existing).
		Error

	if err == nil {
		return sendQQMessage(
			msg.GroupID,
			"你今天已经签到过了。",
			msg.ID,
		)
	}

	record := CheckinRecord{
		GroupID:   msg.GroupID,
		UserID:    msg.UserID,
		Username:  msg.Username,
		Date:      today,
		CreatedAt: time.Now(),
	}

	if err := db.Create(&record).Error; err != nil {
		return sendQQMessage(
			msg.GroupID,
			"签到失败。",
			msg.ID,
		)
	}

	var count int64

	db.Model(&CheckinRecord{}).
		Where(
			"group_id = ? AND user_id = ?",
			msg.GroupID,
			msg.UserID,
		).
		Count(&count)

	return sendQQMessage(
		msg.GroupID,
		fmt.Sprintf(
			"✅ 签到成功！\n累计签到：%d 次",
			count,
		),
		msg.ID,
	)
}

func recordCommand(command string) {
	if command == "" {
		return
	}

	var record struct {
		Command string
		Count   int64
	}

	err := db.Table("command_stats").
		Where("command = ?", command).
		First(&record).
		Error

	if err == gorm.ErrRecordNotFound {
		db.Exec(
			"CREATE TABLE IF NOT EXISTS command_stats (command TEXT PRIMARY KEY, count INTEGER NOT NULL DEFAULT 0)",
		)

		db.Exec(
			"INSERT OR IGNORE INTO command_stats(command,count) VALUES(?,0)",
			command,
		)

		db.Exec(
			"UPDATE command_stats SET count=count+1 WHERE command=?",
			command,
		)

		return
	}

	if err != nil {
		return
	}

	db.Exec(
		"UPDATE command_stats SET count=count+1 WHERE command=?",
		command,
	)
}

func handleWeather(groupID, city, replyID string) {
	apiKey := strings.TrimSpace(
		getConfig("weather_api_key"),
	)

	if apiKey == "" {
		_ = sendQQMessage(
			groupID,
			"天气 API Key 尚未设置。",
			replyID,
		)
		return
	}

	client := resty.New().
		SetTimeout(10 * time.Second)

	resp, err := client.R().
		SetQueryParams(map[string]string{
			"location": city,
			"key":      apiKey,
			"lang":     "zh",
		}).
		Get("https://geoapi.qweather.com/v2/city/lookup")

	if err != nil {
		_ = sendQQMessage(
			groupID,
			"天气查询失败。",
			replyID,
		)
		return
	}

	var geo struct {
		Code     string `json:"code"`
		Location []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"location"`
	}

	if err := json.Unmarshal(resp.Body(), &geo); err != nil {
		return
	}

	if geo.Code != "200" || len(geo.Location) == 0 {
		_ = sendQQMessage(
			groupID,
			"找不到城市："+city,
			replyID,
		)
		return
	}

	locationID := geo.Location[0].ID

	resp, err = client.R().
		SetQueryParams(map[string]string{
			"location": locationID,
			"key":      apiKey,
			"lang":     "zh",
		}).
		Get("https://devapi.qweather.com/v7/weather/now")

	if err != nil {
		return
	}

	var weather struct {
		Code string `json:"code"`
		Now  struct {
			Temp      string `json:"temp"`
			Text      string `json:"text"`
			FeelsLike string `json:"feelsLike"`
			Humidity  string `json:"humidity"`
			WindDir   string `json:"windDir"`
			WindScale string `json:"windScale"`
		} `json:"now"`
	}

	if err := json.Unmarshal(resp.Body(), &weather); err != nil {
		return
	}

	if weather.Code != "200" {
		return
	}

	text := fmt.Sprintf(
		"🌤 %s 当前天气\n天气：%s\n温度：%s℃\n体感：%s℃\n湿度：%s%%\n风向：%s\n风力：%s",
		city,
		weather.Now.Text,
		weather.Now.Temp,
		weather.Now.FeelsLike,
		weather.Now.Humidity,
		weather.Now.WindDir,
		weather.Now.WindScale,
	)

	_ = sendQQMessage(
		groupID,
		text,
		replyID,
	)
}

func apiMessages(c *gin.Context) {
	page, _ := strconv.Atoi(
		c.DefaultQuery("page", "1"),
	)

	size, _ := strconv.Atoi(
		c.DefaultQuery("size", "20"),
	)

	if page < 1 {
		page = 1
	}

	if size < 1 {
		size = 20
	}

	if size > 200 {
		size = 200
	}

	keyword := strings.TrimSpace(
		c.Query("keyword"),
	)

	var records []MessageRecord
	var total int64

	query := db.Model(&MessageRecord{})

	if keyword != "" {
		like := "%" + keyword + "%"

		query = query.Where(
			"group_id LIKE ? OR user_id LIKE ? OR username LIKE ? OR content LIKE ?",
			like,
			like,
			like,
			like,
		)
	}

	query.Count(&total)

	if err := query.
		Order("id DESC").
		Offset((page - 1) * size).
		Limit(size).
		Find(&records).
		Error; err != nil {

		c.JSON(
			http.StatusInternalServerError,
			gin.H{"error": err.Error()},
		)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"page":  page,
		"size":  size,
		"total": total,
		"items": records,
	})
}

func apiSensitiveList(c *gin.Context) {
	var words []SensitiveWord

	if err := db.
		Order("id DESC").
		Find(&words).
		Error; err != nil {

		c.JSON(
			http.StatusInternalServerError,
			gin.H{"error": err.Error()},
		)
		return
	}

	c.JSON(http.StatusOK, words)
}

func apiSensitiveAdd(c *gin.Context) {
	var req struct {
		Word string `json:"word"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(
			http.StatusBadRequest,
			gin.H{"error": "JSON 格式错误"},
		)
		return
	}

	req.Word = strings.TrimSpace(req.Word)

	if req.Word == "" {
		c.JSON(
			http.StatusBadRequest,
			gin.H{"error": "敏感词不能为空"},
		)
		return
	}

	word := SensitiveWord{
		Word: req.Word,
	}

	if err := db.Create(&word).Error; err != nil {
		c.JSON(
			http.StatusBadRequest,
			gin.H{"error": "添加失败，可能已经存在"},
		)
		return
	}

	c.JSON(http.StatusOK, word)
}

func apiSensitiveDelete(c *gin.Context) {
	id, err := strconv.ParseUint(
		c.Param("id"),
		10,
		64,
	)

	if err != nil {
		c.JSON(
			http.StatusBadRequest,
			gin.H{"error": "ID 错误"},
		)
		return
	}

	if err := db.
		Delete(&SensitiveWord{}, id).
		Error; err != nil {

		c.JSON(
			http.StatusInternalServerError,
			gin.H{"error": err.Error()},
		)
		return
	}

	c.JSON(
		http.StatusOK,
		gin.H{"success": true},
	)
}

func apiConfigGet(c *gin.Context) {
	var configs []BotConfig

	if err := db.
		Order("id ASC").
		Find(&configs).
		Error; err != nil {

		c.JSON(
			http.StatusInternalServerError,
			gin.H{"error": err.Error()},
		)
		return
	}

	result := make(map[string]string)

	for _, cfg := range configs {
		result[cfg.ConfigKey] = cfg.ConfigValue
	}

	c.JSON(http.StatusOK, result)
}

func apiConfigPut(c *gin.Context) {
	var values map[string]string

	if err := c.ShouldBindJSON(&values); err != nil {
		c.JSON(
			http.StatusBadRequest,
			gin.H{"error": "JSON 格式错误"},
		)
		return
	}

	qqChanged := false

	for key, value := range values {
		if key == "qq_appid" ||
			key == "qq_secret" ||
			key == "qq_enabled" {
			qqChanged = true
		}

		if _, exists := defaultConfigs[key]; !exists {
			continue
		}

		if err := setConfig(key, value); err != nil {
			c.JSON(
				http.StatusInternalServerError,
				gin.H{"error": err.Error()},
			)
			return
		}
	}

	if qqChanged {
		go syncQQBot()
	}

	c.JSON(
		http.StatusOK,
		gin.H{"success": true},
	)
}

func apiStatus(c *gin.Context) {
	botMu.RLock()
	running := botRunning
	appID := botAppID
	botMu.RUnlock()

	if appID != "" && len(appID) > 8 {
		appID = appID[:8] + "..."
	}

	c.JSON(http.StatusOK, gin.H{
		"name":    QbotName,
		"version": QbotVersion,

		"qq_enabled": getConfig("qq_enabled") == "true",

		"qq_running": running,

		"qq_appid": appID,

		"weather_enabled": getConfig("weather_enabled") == "true",

		"sensitive_enabled": getConfig("sensitive_enabled") == "true",

		"rate_limit_enabled": getConfig("rate_limit_enabled") == "true",

		"command_enabled": getConfig("command_enabled") == "true",
	})
}

func apiStats(c *gin.Context) {
	var totalMessages int64
	var totalCommands int64
	var sensitiveCount int64

	db.Model(&MessageRecord{}).
		Count(&totalMessages)

	db.Model(&MessageRecord{}).
		Where("is_command = ?", true).
		Count(&totalCommands)

	db.Model(&SensitiveWord{}).
		Count(&sensitiveCount)

	type CommandStat struct {
		Command string `json:"command"`
		Count   int64  `json:"count"`
	}

	var commandStats []CommandStat

	db.Table("command_stats").
		Select("command, count").
		Order("count DESC").
		Limit(20).
		Scan(&commandStats)

	c.JSON(http.StatusOK, gin.H{
		"messages":        totalMessages,
		"commands":        totalCommands,
		"sensitive_words": sensitiveCount,
		"command_rank":    commandStats,
	})
}

const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Qbot 管理面板</title>
<style>
*{box-sizing:border-box}
body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif;background:#f5f7fb;color:#222}
.header{background:#1677ff;color:#fff;padding:18px 28px;display:flex;justify-content:space-between;align-items:center}
.header h1{margin:0;font-size:22px}
.container{max-width:1200px;margin:25px auto;padding:0 18px}
.tabs{display:flex;gap:8px;margin-bottom:20px;flex-wrap:wrap}
.tab{border:0;background:#fff;color:#222;padding:11px 18px;border-radius:8px;cursor:pointer}
.tab.active{background:#1677ff;color:#fff}
.panel{background:#fff;border-radius:12px;padding:22px;box-shadow:0 2px 10px rgba(0,0,0,.05);margin-bottom:20px}
h2{margin-top:0}
.row{display:flex;gap:12px;margin-bottom:15px;align-items:center}
.row label{width:180px;flex-shrink:0}
input{width:100%;max-width:600px;padding:10px;border:1px solid #ddd;border-radius:7px}
button{border:0;background:#1677ff;color:#fff;padding:9px 15px;border-radius:7px;cursor:pointer}
button.danger{background:#e5484d}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:15px}
.card{background:#fff;border-radius:10px;padding:18px;box-shadow:0 2px 8px rgba(0,0,0,.04)}
.number{font-size:28px;font-weight:bold;margin-top:8px}
table{width:100%;border-collapse:collapse}
th,td{padding:10px;border-bottom:1px solid #eee;text-align:left}
.word-list{display:flex;flex-wrap:wrap;gap:8px}
.word{padding:7px 10px;background:#f0f2f5;border-radius:6px}
.word button{margin-left:7px;padding:3px 7px;background:#e5484d}
.hidden{display:none}
.notice{padding:12px;background:#fffbe6;border:1px solid #ffe58f;border-radius:7px;margin-bottom:15px}
.switch{display:flex!important;width:auto!important;align-items:center;gap:8px}
.switch input{width:auto}
</style>
</head>
<body>

<div class="header">
<div>
<h1>Qbot</h1>
<div>QQ 群助手 Go 版</div>
</div>
<div id="status">加载中...</div>
</div>

<div class="container">

<div class="tabs">
<button class="tab active" onclick="showTab('dashboard',this)">控制台</button>
<button class="tab" onclick="showTab('qq',this)">QQ机器人</button>
<button class="tab" onclick="showTab('security',this)">群管理</button>
<button class="tab" onclick="showTab('commands',this)">指令</button>
<button class="tab" onclick="showTab('sensitive',this)">敏感词</button>
<button class="tab" onclick="showTab('messages',this)">聊天记录</button>
<button class="tab" onclick="showTab('config',this)">系统配置</button>
</div>

<div id="dashboard" class="tabpage">
<div class="cards">
<div class="card">QQ Bot<div id="cardQQ" class="number">-</div></div>
<div class="card">消息数量<div id="cardMessages" class="number">-</div></div>
<div class="card">指令数量<div id="cardCommands" class="number">-</div></div>
<div class="card">敏感词<div id="cardSensitive" class="number">-</div></div>
</div>

<div class="panel">
<h2>Qbot 状态</h2>
<div id="dashboardStatus">加载中...</div>
</div>
</div>

<div id="qq" class="tabpage hidden">
<div class="panel">
<h2>QQ机器人</h2>

<div class="notice">
不需要 ADMIN_TOKEN，不需要管理员密码。
QQ AppID 和 AppSecret 保存到 Qbot SQLite 数据库。
</div>

<div class="row">
<label>启用 QQ Bot</label>
<label class="switch">
<input id="qq_enabled" type="checkbox">
<span>开启后自动启动</span>
</label>
</div>

<div class="row">
<label>Bot AppID</label>
<input id="qq_appid" placeholder="填写 QQ 机器人 AppID">
</div>

<div class="row">
<label>Bot AppSecret</label>
<input id="qq_secret" type="password" placeholder="填写 QQ 机器人 AppSecret">
</div>

<div class="row">
<label>Webhook 路径</label>
<input value="/qqbot/webhook" readonly>
</div>

<button onclick="saveQQ()">保存 QQ 配置</button>
</div>
</div>

<div id="security" class="tabpage hidden">
<div class="panel">
<h2>群管理</h2>

<div class="row">
<label>敏感词过滤</label>
<input id="sensitive_enabled" type="checkbox">
</div>

<div class="row">
<label>防刷屏</label>
<input id="rate_limit_enabled" type="checkbox">
</div>

<div class="row">
<label>防刷屏间隔（秒）</label>
<input id="rate_limit_seconds" type="number" min="1" max="3600">
</div>

<div class="row">
<label>机器人自动回复</label>
<input id="bot_reply_enabled" type="checkbox">
</div>

<button onclick="saveSecurity()">保存</button>
</div>
</div>

<div id="commands" class="tabpage hidden">
<div class="panel">
<h2>指令设置</h2>

<div class="row">
<label>指令功能</label>
<input id="command_enabled" type="checkbox">
</div>

<div class="row">
<label>指令前缀</label>
<input id="command_prefix">
</div>

<div class="row">
<label>签到功能</label>
<input id="checkin_enabled" type="checkbox">
</div>

<button onclick="saveCommands()">保存</button>
</div>
</div>

<div id="sensitive" class="tabpage hidden">
<div class="panel">
<h2>敏感词管理</h2>

<div class="row">
<input id="newWord" placeholder="输入敏感词">
<button onclick="addWord()">添加</button>
</div>

<div id="wordList" class="word-list"></div>
</div>
</div>

<div id="messages" class="tabpage hidden">
<div class="panel">
<h2>聊天记录</h2>

<div class="row">
<input id="messageKeyword" placeholder="搜索消息">
<button onclick="loadMessages()">搜索</button>
</div>

<table>
<thead>
<tr>
<th>ID</th>
<th>群</th>
<th>用户</th>
<th>内容</th>
<th>指令</th>
<th>时间</th>
</tr>
</thead>
<tbody id="messageTable"></tbody>
</table>
</div>
</div>

<div id="config" class="tabpage hidden">

<div class="panel">
<h2>天气</h2>

<div class="row">
<label>天气功能</label>
<input id="weather_enabled" type="checkbox">
</div>

<div class="row">
<label>和风天气 API Key</label>
<input id="weather_api_key" type="password">
</div>

<button onclick="saveWeather()">保存天气配置</button>
</div>

<div class="panel">
<h2>早安任务</h2>

<div class="row">
<label>启用</label>
<input id="morning_enabled" type="checkbox">
</div>

<div class="row">
<label>时间</label>
<input id="morning_time" type="time">
</div>

<div class="row">
<label>消息</label>
<input id="morning_message">
</div>

<button onclick="saveMorning()">保存</button>
</div>

<div class="panel">
<h2>系统</h2>

<div class="row">
<label>Web 端口</label>
<input id="port">
</div>

<button onclick="savePort()">保存端口</button>
</div>

</div>

</div>

<script>
let config={};

function $(id){return document.getElementById(id)}

function showTab(id,btn){
document.querySelectorAll(".tabpage").forEach(x=>x.classList.add("hidden"));
$(id).classList.remove("hidden");
document.querySelectorAll(".tab").forEach(x=>x.classList.remove("active"));
btn.classList.add("active");
if(id==="messages")loadMessages();
if(id==="sensitive")loadSensitive();
}

async function api(url,options={}){
const r=await fetch(url,options);
if(!r.ok){
throw new Error(await r.text());
}
return r.json();
}

async function loadConfig(){
config=await api("/api/config");

$("qq_enabled").checked=config.qq_enabled==="true";
$("qq_appid").value=config.qq_appid||"";
$("qq_secret").value=config.qq_secret||"";

$("weather_enabled").checked=config.weather_enabled==="true";
$("weather_api_key").value=config.weather_api_key||"";

$("sensitive_enabled").checked=config.sensitive_enabled==="true";
$("rate_limit_enabled").checked=config.rate_limit_enabled==="true";
$("rate_limit_seconds").value=config.rate_limit_seconds||"5";
$("bot_reply_enabled").checked=config.bot_reply_enabled==="true";

$("command_enabled").checked=config.command_enabled==="true";
$("command_prefix").value=config.command_prefix||"/";
$("checkin_enabled").checked=config.checkin_enabled==="true";

$("morning_enabled").checked=config.morning_enabled==="true";
$("morning_time").value=config.morning_time||"08:00";
$("morning_message").value=config.morning_message||"早上好！";

$("port").value=config.port||"8080";
}

async function save(values){
await api("/api/config",{
method:"PUT",
headers:{"Content-Type":"application/json"},
body:JSON.stringify(values)
});
await loadConfig();
await loadStatus();
alert("保存成功");
}

async function saveQQ(){
await save({
qq_enabled:$("qq_enabled").checked?"true":"false",
qq_appid:$("qq_appid").value.trim(),
qq_secret:$("qq_secret").value.trim()
});
}

async function saveSecurity(){
await save({
sensitive_enabled:$("sensitive_enabled").checked?"true":"false",
rate_limit_enabled:$("rate_limit_enabled").checked?"true":"false",
rate_limit_seconds:$("rate_limit_seconds").value,
bot_reply_enabled:$("bot_reply_enabled").checked?"true":"false"
});
}

async function saveCommands(){
await save({
command_enabled:$("command_enabled").checked?"true":"false",
command_prefix:$("command_prefix").value,
checkin_enabled:$("checkin_enabled").checked?"true":"false"
});
}

async function saveWeather(){
await save({
weather_enabled:$("weather_enabled").checked?"true":"false",
weather_api_key:$("weather_api_key").value.trim()
});
}

async function saveMorning(){
await save({
morning_enabled:$("morning_enabled").checked?"true":"false",
morning_time:$("morning_time").value,
morning_message:$("morning_message").value
});
}

async function savePort(){
await save({port:$("port").value});
}

async function loadStatus(){
const s=await api("/api/status");

$("status").textContent=s.qq_running?"QQ Bot 运行中":"QQ Bot 未运行";
$("cardQQ").textContent=s.qq_running?"运行中":"已停止";

$("dashboardStatus").innerHTML=
"版本："+s.version+
"<br>QQ："+(s.qq_enabled?"已开启":"已关闭")+
"<br>QQ运行状态："+(s.qq_running?"运行中":"未运行")+
"<br>天气："+(s.weather_enabled?"开启":"关闭")+
"<br>敏感词："+(s.sensitive_enabled?"开启":"关闭")+
"<br>防刷屏："+(s.rate_limit_enabled?"开启":"关闭")+
"<br>指令："+(s.command_enabled?"开启":"关闭");
}

async function loadStats(){
const s=await api("/api/stats");
$("cardMessages").textContent=s.messages;
$("cardCommands").textContent=s.commands;
$("cardSensitive").textContent=s.sensitive_words;
}

async function loadSensitive(){
const words=await api("/api/sensitive");

$("wordList").innerHTML=words.map(w=>
'<div class="word">'+
escapeHTML(w.word)+
'<button onclick="deleteWord('+w.id+')">删除</button>'+
'</div>'
).join("");
}

async function addWord(){
const word=$("newWord").value.trim();

if(!word)return;

await api("/api/sensitive",{
method:"POST",
headers:{"Content-Type":"application/json"},
body:JSON.stringify({word:word})
});

$("newWord").value="";
await loadSensitive();
await loadStats();
}

async function deleteWord(id){
if(!confirm("确定删除这个敏感词吗？"))return;

await api("/api/sensitive/"+id,{
method:"DELETE"
});

await loadSensitive();
await loadStats();
}

async function loadMessages(){
const keyword=$("messageKeyword").value.trim();

const data=await api(
"/api/messages?page=1&size=100&keyword="+
encodeURIComponent(keyword)
);

$("messageTable").innerHTML=data.items.map(m=>
"<tr>"+
"<td>"+m.id+"</td>"+
"<td>"+escapeHTML(m.group_id||"")+"</td>"+
"<td>"+escapeHTML(m.username||m.user_id||"")+"</td>"+
"<td>"+escapeHTML(m.content||"")+"</td>"+
"<td>"+escapeHTML(m.command||"")+"</td>"+
"<td>"+new Date(m.created_at).toLocaleString()+"</td>"+
"</tr>"
).join("");
}

function escapeHTML(v){
return String(v)
.replaceAll("&","&amp;")
.replaceAll("<","&lt;")
.replaceAll(">","&gt;")
.replaceAll('"',"&quot;")
.replaceAll("'","&#039;");
}

async function refresh(){
try{
await loadConfig();
await loadStatus();
await loadStats();
}catch(e){
console.error(e);
$("status").textContent="连接失败";
}
}

refresh();

setInterval(()=>{
loadStatus().catch(console.error);
},5000);

setInterval(()=>{
loadStats().catch(console.error);
},10000);
</script>

</body>
</html>`

func indexHandler(c *gin.Context) {
	c.Data(
		http.StatusOK,
		"text/html; charset=utf-8",
		[]byte(indexHTML),
	)
}

func setupRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/", indexHandler)

	r.GET("/api/status", apiStatus)
	r.GET("/api/messages", apiMessages)
	r.GET("/api/stats", apiStats)

	r.GET("/api/sensitive", apiSensitiveList)
	r.POST("/api/sensitive", apiSensitiveAdd)
	r.DELETE("/api/sensitive/:id", apiSensitiveDelete)

	r.GET("/api/config", apiConfigGet)
	r.PUT("/api/config", apiConfigPut)

	r.POST("/qqbot/webhook", qqWebhookHandler)

	return r
}

func startCron() {
	c := cron.New()

	_, err := c.AddFunc(
		"* * * * *",
		func() {
			if getConfig("morning_enabled") != "true" {
				return
			}

			target := getConfig("morning_time")
			if target == "" {
				target = "08:00"
			}

			if time.Now().Format("15:04") != target {
				return
			}

			message := getConfig("morning_message")

			if message == "" {
				message = "早上好！"
			}

			logger.Infof(
				"早安任务已触发：%s",
				message,
			)
		},
	)

	if err != nil {
		logger.Warnf("cron 初始化失败: %v", err)
		return
	}

	c.Start()

	logger.Info("定时任务已启动")
}

func initRandom() {
	mathrand.Seed(time.Now().UnixNano())
}

func randomID() string {
	buf := make([]byte, 16)

	if _, err := crand.Read(buf); err != nil {
		return strconv.FormatInt(
			time.Now().UnixNano(),
			10,
		)
	}

	return hex.EncodeToString(buf)
}

func getPort() string {
	port := strings.TrimSpace(getConfig("port"))

	if port == "" {
		port = "8080"
	}

	return port
}

func main() {
	initRandom()

	logger.SetLevel(logrus.InfoLevel)

	logger.Infof(
		"%s v%s 启动中...",
		QbotName,
		QbotVersion,
	)

	if err := initDatabase(); err != nil {
		logger.Fatalf(
			"SQLite 初始化失败: %v",
			err,
		)
	}

	registerQQHandlers()

	if getConfig("qq_enabled") == "true" {
		go syncQQBot()
	}

	startCron()

	router := setupRouter()
	port := getPort()

	logger.Info("================================")
	logger.Infof(
		"Qbot Web 管理面板：http://127.0.0.1:%s",
		port,
	)
	logger.Infof(
		"QQ Webhook：http://127.0.0.1:%s/qqbot/webhook",
		port,
	)
	logger.Info("不需要 ADMIN_TOKEN")
	logger.Info("不需要管理员密码")
	logger.Info("================================")

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil &&
		err != http.ErrServerClosed {
		logger.Fatalf(
			"HTTP 服务启动失败: %v",
			err,
		)
	}
}

var _ = randomID
