package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================================================
// Qbot Web
//
// iOS 3 / Classic iPhone Style
//
// QQ:
//   登录
//   退出登录
//   启动
//   停止
//   状态
//
// AI:
//   OpenAI Compatible
//
// Memory:
//   查看 / 清空
// ============================================================

type QbotWebServer struct {
	server *http.Server

	mu sync.RWMutex

	running bool

	startTime time.Time
}

var qbotWeb *QbotWebServer

// ============================================================
// StartWeb
// ============================================================

func StartWeb() error {

	if !QbotConfig.Web.Enabled {
		log.Println("[Web] WebUI 已关闭")
		return nil
	}

	if qbotWeb != nil {
		return nil
	}

	mux := http.NewServeMux()

	// --------------------------------------------------------
	// 页面
	// --------------------------------------------------------

	mux.HandleFunc(
		"/",
		webIndex,
	)

	// --------------------------------------------------------
	// 系统
	// --------------------------------------------------------

	mux.HandleFunc(
		"/api/status",
		webAPIStatus,
	)

	mux.HandleFunc(
		"/api/info",
		webAPIInfo,
	)

	// --------------------------------------------------------
	// 配置
	// --------------------------------------------------------

	mux.HandleFunc(
		"/api/config",
		webAPIConfig,
	)

	mux.HandleFunc(
		"/api/config/save",
		webAPIConfigSave,
	)

	// --------------------------------------------------------
	// QQ
	// --------------------------------------------------------

	mux.HandleFunc(
		"/api/qq/status",
		webAPIQQStatus,
	)

	mux.HandleFunc(
		"/api/qq/login",
		webAPIQQLogin,
	)

	mux.HandleFunc(
		"/api/qq/logout",
		webAPIQQLogout,
	)

	mux.HandleFunc(
		"/api/qq/start",
		webAPIQQStart,
	)

	mux.HandleFunc(
		"/api/qq/stop",
		webAPIQQStop,
	)

	// --------------------------------------------------------
	// AI
	// --------------------------------------------------------

	mux.HandleFunc(
		"/api/ai/status",
		webAPIAIStatus,
	)

	mux.HandleFunc(
		"/api/ai/chat",
		webAPIAIChat,
	)

	mux.HandleFunc(
		"/api/ai/test",
		webAPIAITest,
	)

	mux.HandleFunc(
		"/api/ai/save",
		webAPIAISave,
	)

	// --------------------------------------------------------
	// Memory
	// --------------------------------------------------------

	mux.HandleFunc(
		"/api/memory",
		webAPIMemory,
	)

	mux.HandleFunc(
		"/api/memory/clear",
		webAPIMemoryClear,
	)

	// --------------------------------------------------------
	// Server
	// --------------------------------------------------------

	addr := fmt.Sprintf(
		"%s:%d",
		QbotConfig.Web.Host,
		QbotConfig.Web.Port,
	)

	server := &http.Server{
		Addr: addr,

		Handler: mux,

		ReadHeaderTimeout:
			10 * time.Second,

		ReadTimeout:
			30 * time.Second,

		WriteTimeout:
			120 * time.Second,

		IdleTimeout:
			60 * time.Second,
	}

	qbotWeb = &QbotWebServer{
		server: server,

		running: true,

		startTime: time.Now(),
	}

	SetWebRunning(true)

	log.Println(
		"[Web] Qbot WebUI:",
		"http://"+addr,
	)

	go func() {

		err := server.ListenAndServe()

		if err != nil &&
			err != http.ErrServerClosed {

			log.Println(
				"[Web]",
				err,
			)

			SetLastError(err)
		}

		SetWebRunning(false)
	}()

	return nil
}

// ============================================================
// StopWeb
// ============================================================

func StopWeb() {

	if qbotWeb == nil {
		return
	}

	qbotWeb.mu.Lock()

	if !qbotWeb.running {

		qbotWeb.mu.Unlock()

		return
	}

	qbotWeb.running = false

	qbotWeb.mu.Unlock()

	log.Println(
		"[Web] 正在关闭",
	)

	if qbotWeb.server != nil {

		if err := qbotWeb.server.Close();
			err != nil {

			log.Println(
				"[Web]",
				err,
			)
		}
	}

	SetWebRunning(false)

	log.Println(
		"[Web] 已停止",
	)
}

// ============================================================
// 首页
// ============================================================

func webIndex(
	w http.ResponseWriter,
	r *http.Request,
) {

	if r.URL.Path != "/" {

		http.NotFound(
			w,
			r,
		)

		return
	}

	w.Header().Set(
		"Content-Type",
		"text/html; charset=utf-8",
	)

	w.Header().Set(
		"Cache-Control",
		"no-store",
	)

	_, _ = w.Write(
		[]byte(
			qbotHTML(),
		),
	)
}

// ============================================================
// Status
// ============================================================

func webAPIStatus(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodGet,
	) {
		return
	}

	writeJSON(
		w,
		http.StatusOK,
		GetRuntimeSnapshot(),
	)
}

// ============================================================
// Info
// ============================================================

func webAPIInfo(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodGet,
	) {
		return
	}

	writeJSON(
		w,
		http.StatusOK,
		map[string]interface{}{
			"name":
				QbotName,

			"version":
				QbotVersion,

			"protocol":
				"OpenAI Compatible",

			"started_at":
				Runtime.StartTime,

			"uptime":
				time.Since(
					Runtime.StartTime,
				).String(),
		},
	)
}

// ============================================================
// Config GET
// ============================================================

func webAPIConfig(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodGet,
	) {
		return
	}

	cfg := QbotConfig

	cfg.QQ.ClientSecret =
		maskSecret(
			cfg.QQ.ClientSecret,
		)

	cfg.QQ.AccessToken =
		maskSecret(
			cfg.QQ.AccessToken,
		)

	cfg.AI.APIKey =
		maskSecret(
			cfg.AI.APIKey,
		)

	writeJSON(
		w,
		http.StatusOK,
		cfg,
	)
}

// ============================================================
// Config Save
// ============================================================

func webAPIConfigSave(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodPost,
	) {
		return
	}

	var incoming Config

	if err := json.NewDecoder(
		r.Body,
	).Decode(
		&incoming,
	); err != nil {

		writeJSON(
			w,
			http.StatusBadRequest,
			map[string]interface{}{
				"ok": false,

				"error":
					"配置格式错误",
			},
		)

		return
	}

	if isMaskedSecret(
		incoming.QQ.ClientSecret,
	) ||
		strings.TrimSpace(
			incoming.QQ.ClientSecret,
		) == "" {

		incoming.QQ.ClientSecret =
			QbotConfig.QQ.ClientSecret
	}

	if isMaskedSecret(
		incoming.QQ.AccessToken,
	) ||
		strings.TrimSpace(
			incoming.QQ.AccessToken,
		) == "" {

		incoming.QQ.AccessToken =
			QbotConfig.QQ.AccessToken
	}

	if isMaskedSecret(
		incoming.AI.APIKey,
	) ||
		strings.TrimSpace(
			incoming.AI.APIKey,
		) == "" {

		incoming.AI.APIKey =
			QbotConfig.AI.APIKey
	}

	if err := SaveConfig(
		ConfigFile,
		incoming,
	); err != nil {

		SetLastError(err)

		writeJSON(
			w,
			http.StatusInternalServerError,
			map[string]interface{}{
				"ok": false,

				"error":
					err.Error(),
			},
		)

		return
	}

	QbotConfig = incoming

	if err := ReloadAI();
		err != nil {

		SetLastError(err)

		writeJSON(
			w,
			http.StatusInternalServerError,
			map[string]interface{}{
				"ok": false,

				"error":
					"配置已保存，但 AI 更新失败: " +
						err.Error(),
			},
		)

		return
	}

	writeJSON(
		w,
		http.StatusOK,
		map[string]interface{}{
			"ok": true,

			"message":
				"设置已保存",
		},
	)
}

// ============================================================
// QQ Status
// ============================================================

func webAPIQQStatus(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodGet,
	) {
		return
	}

	QbotContext.RLock()

	account :=
		QbotContext.QQAccount

	nickname :=
		QbotContext.QQNickname

	status :=
		QbotContext.QQStatus

	connected :=
		QbotContext.QQConnected

	QbotContext.RUnlock()

	adapter :=
		GetQQAdapter()

	loggedIn := false

	if adapter != nil {

		// 当前 Adapter 公开接口只有 IsConnected。
		// 连接状态可以作为登录后的实际运行状态。
		loggedIn =
			adapter.IsConnected()
	}

	writeJSON(
		w,
		http.StatusOK,
		map[string]interface{}{
			"enabled":
				QbotConfig.QQ.Enabled,

			"provider":
				QbotConfig.QQ.Provider,

			"connected":
				connected,

			"logged_in":
				loggedIn,

			"account":
				account,

			"nickname":
				nickname,

			"status":
				status,

			"auto_reconnect":
				QbotConfig.QQ.AutoReconnect,
		},
	)
}

// ============================================================
// QQ Login
// ============================================================

func webAPIQQLogin(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodPost,
	) {
		return
	}

	// --------------------------------------------------------
	// 如果已经在线
	// --------------------------------------------------------

	if IsQQConnected() {

		writeJSON(
			w,
			http.StatusOK,
			map[string]interface{}{
				"ok": true,

				"logged_in": true,

				"connected": true,

				"message":
					"QQ 已经登录并在线",
			},
		)

		return
	}

	// --------------------------------------------------------
	// QQ Client
	// --------------------------------------------------------

	adapter :=
		GetQQAdapter()

	if adapter == nil {

		writeJSON(
			w,
			http.StatusServiceUnavailable,
			map[string]interface{}{
				"ok": false,

				"logged_in": false,

				"error":
					"QQ Client 尚未初始化",
			},
		)

		return
	}

	// --------------------------------------------------------
	// 登录
	// --------------------------------------------------------

	account :=
		strings.TrimSpace(
			QbotConfig.QQ.Account,
		)

	if account == "" {

		writeJSON(
			w,
			http.StatusBadRequest,
			map[string]interface{}{
				"ok": false,

				"error":
					"请先在设置中填写 QQ 账号",
			},
		)

		return
	}

	SetQQStatus(
		account,
		"",
		"logging_in",
		false,
	)

	log.Println(
		"[Web][QQ] 登录请求:",
		account,
	)

	if err := adapter.Login();
		err != nil {

		SetQQStatus(
			account,
			"",
			"login_failed",
			false,
		)

		SetLastError(err)

		writeJSON(
			w,
			http.StatusBadGateway,
			map[string]interface{}{
				"ok": false,

				"logged_in": false,

				"error":
					err.Error(),
			},
		)

		return
	}

	SetQQStatus(
		account,
		"",
		"logged_in",
		false,
	)

	writeJSON(
		w,
		http.StatusOK,
		map[string]interface{}{
			"ok": true,

			"logged_in": true,

			"connected":
				adapter.IsConnected(),

			"account":
				account,

			"message":
				"QQ Client 登录初始化成功，请继续启动连接",
		},
	)
}

// ============================================================
// QQ Logout
// ============================================================

func webAPIQQLogout(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodPost,
	) {
		return
	}

	adapter :=
		GetQQAdapter()

	if adapter == nil {

		SetQQStatus(
			"",
			"",
			"offline",
			false,
		)

		writeJSON(
			w,
			http.StatusOK,
			map[string]interface{}{
				"ok": true,

				"logged_in": false,

				"message":
					"QQ Client 已退出",
			},
		)

		return
	}

	if err := adapter.Disconnect();
		err != nil {

		log.Println(
			"[Web][QQ] Disconnect:",
			err,
		)
	}

	if err := adapter.Logout();
		err != nil {

		SetLastError(err)

		writeJSON(
			w,
			http.StatusInternalServerError,
			map[string]interface{}{
				"ok": false,

				"error":
					err.Error(),
			},
		)

		return
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

	writeJSON(
		w,
		http.StatusOK,
		map[string]interface{}{
			"ok": true,

			"logged_in": false,

			"connected": false,

			"message":
				"QQ 已退出登录",
		},
	)
}

// ============================================================
// QQ Start
// ============================================================

func webAPIQQStart(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodPost,
	) {
		return
	}

	if IsQQConnected() {

		writeJSON(
			w,
			http.StatusOK,
			map[string]interface{}{
				"ok": true,

				"running": true,

				"message":
					"QQ 已经在线",
			},
		)

		return
	}

	go func() {

		if err := StartQQ();
			err != nil {

			log.Println(
				"[Web][QQ]",
				err,
			)

			SetLastError(err)
		}

	}()

	writeJSON(
		w,
		http.StatusOK,
		map[string]interface{}{
			"ok": true,

			"starting": true,

			"message":
				"正在启动 QQ Client",
		},
	)
}

// ============================================================
// QQ Stop
// ============================================================

func webAPIQQStop(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodPost,
	) {
		return
	}

	StopQQ()

	writeJSON(
		w,
		http.StatusOK,
		map[string]interface{}{
			"ok": true,

			"connected":
				false,
		},
	)
}

// ============================================================
// AI Status
// ============================================================

func webAPIAIStatus(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodGet,
	) {
		return
	}

	writeJSON(
		w,
		http.StatusOK,
		GetAIStatus(),
	)
}

// ============================================================
// AI Test
// ============================================================

func webAPIAITest(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodPost,
	) {
		return
	}

	var req struct {
		Message string `json:"message"`
	}

	if err := json.NewDecoder(
		r.Body,
	).Decode(
		&req,
	); err != nil {

		writeJSON(
			w,
			http.StatusBadRequest,
			map[string]interface{}{
				"ok": false,

				"error":
					"请求格式错误",
			},
		)

		return
	}

	req.Message =
		strings.TrimSpace(
			req.Message,
		)

	if req.Message == "" {

		req.Message =
			"你好，请回复一句话测试 Qbot AI。"
	}

	reply, err :=
		TestAI(
			req.Message,
		)

	if err != nil {

		SetLastError(err)

		writeJSON(
			w,
			http.StatusBadGateway,
			map[string]interface{}{
				"ok": false,

				"error":
					err.Error(),
			},
		)

		return
	}

	writeJSON(
		w,
		http.StatusOK,
		map[string]interface{}{
			"ok": true,

			"reply":
				reply,
		},
	)
}

// ============================================================
// AI Chat
// ============================================================

func webAPIAIChat(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodPost,
	) {
		return
	}

	var request struct {

		Message string `json:"message"`

		UserID string `json:"user_id"`

		GroupID string `json:"group_id"`
	}

	if err := json.NewDecoder(
		r.Body,
	).Decode(
		&request,
	); err != nil {

		writeJSON(
			w,
			http.StatusBadRequest,
			map[string]interface{}{
				"ok": false,

				"error":
					"请求格式错误",
			},
		)

		return
	}

	request.Message =
		strings.TrimSpace(
			request.Message,
		)

	if request.Message == "" {

		writeJSON(
			w,
			http.StatusBadRequest,
			map[string]interface{}{
				"ok": false,

				"error":
					"消息不能为空",
			},
		)

		return
	}

	reply, err :=
		AskAI(
			request.Message,
			request.UserID,
			request.GroupID,
		)

	if err != nil {

		SetLastError(err)

		writeJSON(
			w,
			http.StatusBadGateway,
			map[string]interface{}{
				"ok": false,

				"error":
					err.Error(),
			},
		)

		return
	}

	writeJSON(
		w,
		http.StatusOK,
		map[string]interface{}{
			"ok": true,

			"reply":
				reply,

			"model":
				QbotConfig.AI.Model,
		},
	)
}

// ============================================================
// AI Save
// ============================================================

func webAPIAISave(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodPost,
	) {
		return
	}

	var data struct {

		Enabled *bool `json:"enabled"`

		BaseURL string `json:"base_url"`

		APIKey string `json:"api_key"`

		Model string `json:"model"`

		Temperature *float64 `json:"temperature"`

		MaxTokens *int `json:"max_tokens"`

		SystemPrompt string `json:"system_prompt"`

		MemoryEnabled *bool `json:"memory_enabled"`
	}

	if err := json.NewDecoder(
		r.Body,
	).Decode(
		&data,
	); err != nil {

		writeJSON(
			w,
			http.StatusBadRequest,
			map[string]interface{}{
				"ok": false,

				"error":
					"AI 配置格式错误",
			},
		)

		return
	}

	if data.Enabled != nil {

		QbotConfig.AI.Enabled =
			*data.Enabled
	}

	if strings.TrimSpace(
		data.BaseURL,
	) != "" {

		QbotConfig.AI.BaseURL =
			strings.TrimSpace(
				data.BaseURL,
			)
	}

	if strings.TrimSpace(
		data.APIKey,
	) != "" &&
		!isMaskedSecret(
			data.APIKey,
		) {

		QbotConfig.AI.APIKey =
			strings.TrimSpace(
				data.APIKey,
			)
	}

	if strings.TrimSpace(
		data.Model,
	) != "" {

		QbotConfig.AI.Model =
			strings.TrimSpace(
				data.Model,
			)
	}

	if data.Temperature != nil {

		value :=
			*data.Temperature

		if value < 0 {
			value = 0
		}

		if value > 2 {
			value = 2
		}

		QbotConfig.AI.Temperature =
			value
	}

	if data.MaxTokens != nil {

		value :=
			*data.MaxTokens

		if value < 1 {
			value = 1
		}

		QbotConfig.AI.MaxTokens =
			value
	}

	QbotConfig.AI.SystemPrompt =
		data.SystemPrompt

	if data.MemoryEnabled != nil {

		QbotConfig.AI.MemoryEnabled =
			*data.MemoryEnabled
	}

	if err := SaveConfig(
		ConfigFile,
		QbotConfig,
	); err != nil {

		SetLastError(err)

		writeJSON(
			w,
			http.StatusInternalServerError,
			map[string]interface{}{
				"ok": false,

				"error":
					err.Error(),
			},
		)

		return
	}

	if err := ReloadAI();
		err != nil {

		SetLastError(err)

		writeJSON(
			w,
			http.StatusBadGateway,
			map[string]interface{}{
				"ok": false,

				"error":
					"设置已保存，但 AI 初始化失败: " +
						err.Error(),
			},
		)

		return
	}

	writeJSON(
		w,
		http.StatusOK,
		map[string]interface{}{
			"ok": true,

			"message":
				"AI 设置已保存",
		},
	)
}

// ============================================================
// Memory
// ============================================================

func webAPIMemory(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodGet,
	) {
		return
	}

	content, err :=
		ReadMemory()

	if err != nil {

		writeJSON(
			w,
			http.StatusInternalServerError,
			map[string]interface{}{
				"ok": false,

				"error":
					err.Error(),
			},
		)

		return
	}

	writeJSON(
		w,
		http.StatusOK,
		map[string]interface{}{
			"ok": true,

			"enabled":
				QbotConfig.Memory.Enabled,

			"file":
				QbotConfig.Memory.File,

			"count":
				MemoryCount(),

			"content":
				content,
		},
	)
}

// ============================================================
// Memory Clear
// ============================================================

func webAPIMemoryClear(
	w http.ResponseWriter,
	r *http.Request,
) {

	if !allowMethod(
		w,
		r,
		http.MethodPost,
	) {
		return
	}

	if err := ClearMemory();
		err != nil {

		SetLastError(err)

		writeJSON(
			w,
			http.StatusInternalServerError,
			map[string]interface{}{
				"ok": false,

				"error":
					err.Error(),
			},
		)

		return
	}

	writeJSON(
		w,
		http.StatusOK,
		map[string]interface{}{
			"ok": true,

			"message":
				"Memory 已清空",
		},
	)
}

// ============================================================
// HTTP
// ============================================================

func allowMethod(
	w http.ResponseWriter,
	r *http.Request,
	method string,
) bool {

	if r.Method == method {
		return true
	}

	w.Header().Set(
		"Allow",
		method,
	)

	writeJSON(
		w,
		http.StatusMethodNotAllowed,
		map[string]interface{}{
			"ok": false,

			"error":
				"Method Not Allowed",
		},
	)

	return false
}

// ============================================================
// JSON
// ============================================================

func writeJSON(
	w http.ResponseWriter,
	status int,
	value interface{},
) {

	w.Header().Set(
		"Content-Type",
		"application/json; charset=utf-8",
	)

	w.WriteHeader(status)

	_ = json.NewEncoder(
		w,
	).Encode(value)
}

// ============================================================
// Secret
// ============================================================

func maskSecret(
	value string,
) string {

	value =
		strings.TrimSpace(value)

	if value == "" {
		return ""
	}

	if len(value) <= 8 {
		return "••••••••"
	}

	return value[:4] +
		"••••••••" +
		value[len(value)-4:]
}

func isMaskedSecret(
	value string,
) bool {

	return strings.Contains(
		value,
		"••••",
	)
}

// ============================================================
// HTML
// ============================================================

func qbotHTML() string {

	port :=
		strconv.Itoa(
			QbotConfig.Web.Port,
		)

	return `<!DOCTYPE html>
<html lang="zh-CN">

<head>

<meta charset="UTF-8">

<meta
 name="viewport"
 content="width=device-width,
 initial-scale=1,
 maximum-scale=1,
 user-scalable=no">

<title>Qbot</title>

<style>

* {
    box-sizing: border-box;
    -webkit-tap-highlight-color: transparent;
}

html,
body {

    margin: 0;
    padding: 0;

    width: 100%;
    min-height: 100%;

    background:
        linear-gradient(
            #cfd1d5,
            #bfc1c5
        );

    font-family:
        Helvetica,
        Arial,
        "Microsoft YaHei",
        sans-serif;

    color: #111;

    font-size: 14px;
}

body {
    min-height: 100vh;
}

.phone {

    width: 100%;
    max-width: 390px;

    min-height: 100vh;

    margin: 0 auto;

    background:
        linear-gradient(
            #f3f3f3,
            #dedede
        );

    box-shadow:
        0 0 25px
        rgba(0,0,0,.35);
}

/* =========================================================
   Status Bar
   ========================================================= */

.statusbar {

    height: 20px;

    padding:
        2px 7px;

    background:
        linear-gradient(
            #5c5c5c,
            #181818
        );

    color: #fff;

    font-size: 11px;

    text-shadow:
        0 -1px 0 #000;

    display: flex;

    align-items: center;

    justify-content: space-between;
}

/* =========================================================
   Navigation
   ========================================================= */

.navbar {

    height: 44px;

    background:
        linear-gradient(
            #7897b9 0%,
            #4c719b 45%,
            #31577f 50%,
            #456b94 100%
        );

    border-bottom:
        1px solid #173b5d;

    box-shadow:
        inset 0 1px 0
        rgba(255,255,255,.5);

    color: #fff;

    display: flex;

    align-items: center;

    justify-content: center;

    position: relative;

    text-shadow:
        0 -1px 1px #1c3550;

    font-size: 20px;

    font-weight: bold;
}

.nav-title {

    overflow: hidden;

    white-space: nowrap;

    text-overflow: ellipsis;
}

.nav-button {

    position: absolute;

    top: 7px;

    min-width: 55px;

    height: 30px;

    padding:
        0 10px;

    border-radius: 6px;

    border:
        1px solid #1e3e5d;

    background:
        linear-gradient(
            #7998b9,
            #456a91
        );

    box-shadow:
        inset 0 1px 0
        rgba(255,255,255,.45);

    color: #fff;

    font-weight: bold;

    text-shadow:
        0 -1px #1d3349;

    cursor: pointer;
}

.nav-button:active {

    background:
        linear-gradient(
            #3d5d7c,
            #6987a5
        );
}

.nav-left {
    left: 6px;
}

/* =========================================================
   Content
   ========================================================= */

.content {

    padding:
        12px 10px 25px;

    min-height:
        calc(100vh - 64px);
}

/* =========================================================
   Pages
   ========================================================= */

.page {
    display: none;
}

.page.active {
    display: block;
}

/* =========================================================
   Section
   ========================================================= */

.section-title {

    margin:
        14px 8px 5px;

    color: #4c4c4c;

    font-size: 13px;

    font-weight: bold;

    text-shadow:
        0 1px #fff;
}

.group {

    margin-bottom: 15px;

    overflow: hidden;

    border:
        1px solid #aaa;

    border-radius: 9px;

    background: #fff;

    box-shadow:
        0 1px 2px
        rgba(0,0,0,.18);
}

/* =========================================================
   Cell
   ========================================================= */

.cell {

    min-height: 44px;

    padding:
        8px 12px;

    display: flex;

    align-items: center;

    position: relative;

    border-bottom:
        1px solid #c7c7c7;

    background:
        linear-gradient(
            #fff,
            #f4f4f4
        );

    cursor: pointer;
}

.cell:last-child {
    border-bottom: 0;
}

.cell:active {

    background:
        linear-gradient(
            #d8d8d8,
            #eeeeee
        );
}

.cell-title {

    flex: 1;

    font-size: 15px;

    color: #111;
}

.cell-value {

    max-width: 58%;

    color: #777;

    text-align: right;

    overflow: hidden;

    text-overflow: ellipsis;

    white-space: nowrap;
}

.chevron {

    margin-left: 7px;

    color: #aaa;

    font-size: 21px;

    line-height: 15px;
}

/* =========================================================
   Input
   ========================================================= */

.input {

    width: 100%;

    height: 38px;

    padding:
        7px 9px;

    border:
        1px solid #999;

    border-radius: 7px;

    background: #fff;

    box-shadow:
        inset 0 1px 2px
        rgba(0,0,0,.15);

    font-size: 14px;

    outline: none;
}

.input:focus {

    border-color:
        #5489ba;

    box-shadow:
        0 0 3px
        rgba(60,130,200,.45);
}

/* =========================================================
   Textarea
   ========================================================= */

.textarea {

    width: 100%;

    min-height: 110px;

    padding: 8px;

    resize: vertical;

    border:
        1px solid #999;

    border-radius: 7px;

    background: #fff;

    font-family:
        Helvetica,
        Arial,
        "Microsoft YaHei",
        sans-serif;

    font-size: 14px;

    box-shadow:
        inset 0 1px 2px
        rgba(0,0,0,.15);

    outline: none;
}

/* =========================================================
   Button
   ========================================================= */

.button {

    width: 100%;

    min-height: 42px;

    border-radius: 8px;

    border:
        1px solid #1c4770;

    background:
        linear-gradient(
            #8eb0cf,
            #527ca4
        );

    color: #fff;

    font-weight: bold;

    font-size: 15px;

    text-shadow:
        0 -1px #274765;

    box-shadow:
        inset 0 1px
        rgba(255,255,255,.55),
        0 1px 2px
        rgba(0,0,0,.25);

    cursor: pointer;
}

.button:active {

    background:
        linear-gradient(
            #456988,
            #7898b4
        );
}

.button.green {

    border-color:
        #3c7d21;

    background:
        linear-gradient(
            #8acb5c,
            #4d9a24
        );
}

.button.red {

    border-color:
        #8b2525;

    background:
        linear-gradient(
            #e77676,
            #b43d3d
        );

    text-shadow:
        0 -1px #6e1f1f;
}

.form-row {

    padding:
        10px 12px;

    border-bottom:
        1px solid #ccc;
}

.form-row:last-child {
    border-bottom: 0;
}

.form-label {

    margin-bottom: 5px;

    font-size: 12px;

    color: #666;

    font-weight: bold;
}

/* =========================================================
   Status
   ========================================================= */

.status {

    display: inline-block;

    padding:
        3px 8px;

    border-radius:
        10px;

    font-size:
        12px;

    background:
        #aaa;

    color:
        #fff;

    text-shadow:
        0 -1px #666;
}

.status.online {

    background:
        linear-gradient(
            #73bd3e,
            #4b9821
        );
}

.status.offline {

    background:
        linear-gradient(
            #999,
            #777
        );
}

.status.login {

    background:
        linear-gradient(
            #e0a83d,
            #b87912
        );
}

/* =========================================================
   Hero
   ========================================================= */

.hero {

    text-align: center;

    padding:
        20px 10px 15px;
}

.hero-icon {

    width: 72px;

    height: 72px;

    margin: auto;

    border-radius: 16px;

    background:
        linear-gradient(
            #777,
            #222
        );

    border:
        1px solid #111;

    box-shadow:
        inset 0 1px
        rgba(255,255,255,.5),
        0 2px 4px
        rgba(0,0,0,.35);

    display: flex;

    align-items: center;

    justify-content: center;

    color: #fff;

    font-size: 32px;

    font-weight: bold;
}

.hero-title {

    margin-top: 8px;

    font-size: 22px;

    font-weight: bold;
}

.hero-subtitle {

    margin-top: 3px;

    color: #666;

    font-size: 12px;
}

.ai-result {

    white-space: pre-wrap;

    word-break: break-word;

    padding: 10px;

    min-height: 45px;

    border-radius: 7px;

    background: #f7f7f7;

    border:
        1px solid #ccc;

    font-size: 14px;
}

.footer {

    text-align: center;

    color: #777;

    font-size: 11px;

    text-shadow:
        0 1px #fff;

    padding:
        8px 0 20px;
}

</style>

</head>

<body>

<div class="phone">

<div class="statusbar">

    <span id="clock">
        12:00
    </span>

    <span>
        Qbot
    </span>

    <span id="topQQ">
        ●
    </span>

</div>

<div class="navbar">

    <button
        id="backButton"
        class="nav-button nav-left"
        style="display:none"
        onclick="goHome()">

        ‹ 返回

    </button>

    <div
        id="navTitle"
        class="nav-title">

        Qbot

    </div>

</div>

<div class="content">

<!-- =====================================================
     HOME
     ===================================================== -->

<div
    id="page-home"
    class="page active">

    <div class="hero">

        <div class="hero-icon">
            Q
        </div>

        <div class="hero-title">
            Qbot
        </div>

        <div class="hero-subtitle">
            QQ Third-Party Client
        </div>

    </div>

    <div class="section-title">
        Qbot
    </div>

    <div class="group">

        <div
            class="cell"
            onclick="showPage('status')">

            <div class="cell-title">
                状态
            </div>

            <div
                id="homeStatus"
                class="status online">
                在线
            </div>

            <div class="chevron">
                ›
            </div>

        </div>

        <div
            class="cell"
            onclick="showPage('qq')">

            <div class="cell-title">
                QQ
            </div>

            <div
                id="homeQQ"
                class="cell-value">
                未登录
            </div>

            <div class="chevron">
                ›
            </div>

        </div>

        <div
            class="cell"
            onclick="showPage('ai')">

            <div class="cell-title">
                AI
            </div>

            <div
                id="homeAI"
                class="cell-value">
                未启用
            </div>

            <div class="chevron">
                ›
            </div>

        </div>

        <div
            class="cell"
            onclick="showPage('memory')">

            <div class="cell-title">
                Memory
            </div>

            <div
                id="homeMemory"
                class="cell-value">
                0
            </div>

            <div class="chevron">
                ›
            </div>

        </div>

    </div>

    <div class="section-title">
        设置
    </div>

    <div class="group">

        <div
            class="cell"
            onclick="showPage('settings')">

            <div class="cell-title">
                设置
            </div>

            <div class="chevron">
                ›
            </div>

        </div>

        <div
            class="cell"
            onclick="showPage('about')">

            <div class="cell-title">
                关于 Qbot
            </div>

            <div class="chevron">
                ›
            </div>

        </div>

    </div>

</div>

<!-- =====================================================
     STATUS
     ===================================================== -->

<div
    id="page-status"
    class="page">

    <div class="section-title">
        Qbot 状态
    </div>

    <div class="group">

        <div class="cell">

            <div class="cell-title">
                Qbot
            </div>

            <div
                id="statusQbot"
                class="status online">
                在线
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                QQ
            </div>

            <div
                id="statusQQ"
                class="status offline">
                离线
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                AI
            </div>

            <div
                id="statusAI"
                class="status offline">
                未就绪
            </div>

        </div>

    </div>

    <div class="section-title">
        统计
    </div>

    <div class="group">

        <div class="cell">

            <div class="cell-title">
                接收消息
            </div>

            <div
                id="statMessages"
                class="cell-value">
                0
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                发送消息
            </div>

            <div
                id="statSent"
                class="cell-value">
                0
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                AI 消息
            </div>

            <div
                id="statAI"
                class="cell-value">
                0
            </div>

        </div>

    </div>

</div>

<!-- =====================================================
     QQ
     ===================================================== -->

<div
    id="page-qq"
    class="page">

    <div class="section-title">
        QQ
    </div>

    <div class="group">

        <div class="cell">

            <div class="cell-title">
                登录状态
            </div>

            <div
                id="qqPageStatus"
                class="status offline">
                未登录
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                QQ账号
            </div>

            <div
                id="qqAccount"
                class="cell-value">
                -
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                昵称
            </div>

            <div
                id="qqNickname"
                class="cell-value">
                -
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                连接状态
            </div>

            <div
                id="qqConnection"
                class="cell-value">
                离线
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                Provider
            </div>

            <div
                id="qqProvider"
                class="cell-value">
                -
            </div>

        </div>

    </div>

    <div class="section-title">
        QQ 登录
    </div>

    <div class="group">

        <div class="form-row">

            <div class="form-label">
                QQ账号
            </div>

            <input
                id="qqLoginAccount"
                class="input"
                type="text"
                inputmode="numeric"
                placeholder="请输入 QQ 账号">

        </div>

    </div>

    <div class="group">

        <div class="form-row">

            <button
                id="qqLoginButton"
                class="button green"
                onclick="qqLogin()">

                登录 QQ

            </button>

        </div>

        <div class="form-row">

            <button
                class="button"
                onclick="qqStart()">

                启动 QQ

            </button>

        </div>

        <div class="form-row">

            <button
                class="button red"
                onclick="qqLogout()">

                退出登录

            </button>

        </div>

        <div class="form-row">

            <button
                class="button red"
                onclick="qqStop()">

                停止 QQ

            </button>

        </div>

    </div>

    <div class="section-title">
        登录信息
    </div>

    <div class="group">

        <div class="form-row">

            <div
                id="qqLoginResult"
                class="ai-result">

                尚未执行登录操作

            </div>

        </div>

    </div>

</div>

<!-- =====================================================
     AI
     ===================================================== -->

<div
    id="page-ai"
    class="page">

    <div class="section-title">
        AI
    </div>

    <div class="group">

        <div class="cell">

            <div class="cell-title">
                AI
            </div>

            <div
                id="aiPageStatus"
                class="status offline">
                未启用
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                协议
            </div>

            <div class="cell-value">
                OpenAI Compatible
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                模型
            </div>

            <div
                id="aiPageModel"
                class="cell-value">
                -
            </div>

        </div>

    </div>

    <div class="section-title">
        OpenAI Compatible
    </div>

    <div class="group">

        <div class="form-row">

            <div class="form-label">
                API 地址
            </div>

            <input
                id="aiBaseURL"
                class="input"
                type="text"
                placeholder="https://api.openai.com/v1">

        </div>

        <div class="form-row">

            <div class="form-label">
                API Key
            </div>

            <input
                id="aiAPIKey"
                class="input"
                type="password"
                placeholder="API Key">

        </div>

        <div class="form-row">

            <div class="form-label">
                模型
            </div>

            <input
                id="aiModel"
                class="input"
                type="text"
                placeholder="gpt-4o-mini">

        </div>

        <div class="form-row">

            <div class="form-label">
                Temperature
            </div>

            <input
                id="aiTemperature"
                class="input"
                type="number"
                min="0"
                max="2"
                step="0.1">

        </div>

        <div class="form-row">

            <div class="form-label">
                Max Tokens
            </div>

            <input
                id="aiMaxTokens"
                class="input"
                type="number"
                min="1"
                step="1">

        </div>

        <div class="form-row">

            <div class="form-label">
                System Prompt
            </div>

            <textarea
                id="aiSystemPrompt"
                class="textarea"></textarea>

        </div>

    </div>

    <div class="group">

        <div class="form-row">

            <button
                class="button"
                onclick="saveAI()">

                保存 AI 设置

            </button>

        </div>

        <div class="form-row">

            <button
                class="button"
                onclick="testAI()">

                测试 AI

            </button>

        </div>

    </div>

    <div class="section-title">
        测试结果
    </div>

    <div class="group">

        <div class="form-row">

            <div
                id="aiResult"
                class="ai-result">

                尚未测试

            </div>

        </div>

    </div>

</div>

<!-- =====================================================
     MEMORY
     ===================================================== -->

<div
    id="page-memory"
    class="page">

    <div class="section-title">
        Memory
    </div>

    <div class="group">

        <div class="cell">

            <div class="cell-title">
                状态
            </div>

            <div
                id="memoryStatus"
                class="cell-value">
                -
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                条目
            </div>

            <div
                id="memoryCount"
                class="cell-value">
                0
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                文件
            </div>

            <div
                id="memoryFile"
                class="cell-value">
                -
            </div>

        </div>

    </div>

    <div class="section-title">
        内容
    </div>

    <div class="group">

        <div class="form-row">

            <textarea
                id="memoryContent"
                class="textarea"
                readonly></textarea>

        </div>

    </div>

    <div class="group">

        <div class="form-row">

            <button
                class="button"
                onclick="loadMemory()">

                刷新 Memory

            </button>

        </div>

        <div class="form-row">

            <button
                class="button red"
                onclick="clearMemory()">

                清空 Memory

            </button>

        </div>

    </div>

</div>

<!-- =====================================================
     SETTINGS
     ===================================================== -->

<div
    id="page-settings"
    class="page">

    <div class="section-title">
        Qbot 设置
    </div>

    <div class="group">

        <div
            class="cell"
            onclick="showPage('qq')">

            <div class="cell-title">
                QQ
            </div>

            <div
                id="settingsQQ"
                class="cell-value">
                未登录
            </div>

            <div class="chevron">
                ›
            </div>

        </div>

        <div
            class="cell"
            onclick="showPage('ai')">

            <div class="cell-title">
                AI
            </div>

            <div class="cell-value">
                OpenAI Compatible
            </div>

            <div class="chevron">
                ›
            </div>

        </div>

        <div
            class="cell"
            onclick="showPage('memory')">

            <div class="cell-title">
                Memory
            </div>

            <div class="chevron">
                ›
            </div>

        </div>

    </div>

    <div class="section-title">
        Web
    </div>

    <div class="group">

        <div class="cell">

            <div class="cell-title">
                Web 地址
            </div>

            <div class="cell-value">
                127.0.0.1:` + port + `
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                WebSocket
            </div>

            <div class="cell-value">
                ` + boolText(
		QbotConfig.Web.EnableWebSocket,
	) + `
            </div>

        </div>

    </div>

</div>

<!-- =====================================================
     ABOUT
     ===================================================== -->

<div
    id="page-about"
    class="page">

    <div class="hero">

        <div class="hero-icon">
            Q
        </div>

        <div class="hero-title">
            Qbot
        </div>

        <div class="hero-subtitle">
            ` + QbotVersion + `
        </div>

    </div>

    <div class="section-title">
        关于
    </div>

    <div class="group">

        <div class="cell">

            <div class="cell-title">
                名称
            </div>

            <div class="cell-value">
                Qbot
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                类型
            </div>

            <div class="cell-value">
                QQ Third-Party Client
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                AI Protocol
            </div>

            <div class="cell-value">
                OpenAI Compatible
            </div>

        </div>

        <div class="cell">

            <div class="cell-title">
                Version
            </div>

            <div class="cell-value">
                ` + QbotVersion + `
            </div>

        </div>

    </div>

</div>

<div class="footer">
    Qbot · ` + QbotVersion + `
</div>

</div>

<script>

/* ==========================================================
   页面
   ========================================================== */

const pageTitles = {

    home: "Qbot",

    status: "状态",

    qq: "QQ",

    ai: "AI",

    memory: "Memory",

    settings: "设置",

    about: "关于 Qbot"
};

function showPage(name) {

    document
        .querySelectorAll(".page")
        .forEach(function(page) {

            page.classList.remove("active");

        });

    const page =
        document.getElementById(
            "page-" + name
        );

    if (!page) {
        return;
    }

    page.classList.add("active");

    document
        .getElementById("navTitle")
        .textContent =
            pageTitles[name] || "Qbot";

    const back =
        document.getElementById(
            "backButton"
        );

    if (name === "home") {

        back.style.display =
            "none";

    } else {

        back.style.display =
            "block";
    }

    if (name === "ai") {
        loadAI();
    }

    if (name === "memory") {
        loadMemory();
    }

    if (name === "qq") {
        loadQQ();
    }

    if (name === "status") {
        updateStatus();
    }
}

function goHome() {
    showPage("home");
}

/* ==========================================================
   Clock
   ========================================================== */

function updateClock() {

    const now =
        new Date();

    let h =
        now.getHours()
            .toString()
            .padStart(2, "0");

    let m =
        now.getMinutes()
            .toString()
            .padStart(2, "0");

    document
        .getElementById("clock")
        .textContent =
            h + ":" + m;
}

updateClock();

setInterval(
    updateClock,
    1000
);

/* ==========================================================
   JSON
   ========================================================== */

async function requestJSON(
    url,
    options
) {

    const response =
        await fetch(
            url,
            options
        );

    const text =
        await response.text();

    let data = {};

    try {

        data =
            text
                ? JSON.parse(text)
                : {};

    } catch (e) {

        throw new Error(
            "服务器返回了无效数据"
        );
    }

    if (!response.ok) {

        throw new Error(
            data.error ||
            "HTTP " +
            response.status
        );
    }

    return data;
}

/* ==========================================================
   Status
   ========================================================== */

async function updateStatus() {

    try {

        const data =
            await requestJSON(
                "/api/status"
            );

        const qqOnline =
            !!data.qq_connected;

        const aiReady =
            !!data.ai_ready;

        document
            .getElementById(
                "homeStatus"
            )
            .textContent =
                data.running
                    ? "在线"
                    : "停止";

        document
            .getElementById(
                "homeStatus"
            )
            .className =
                data.running
                    ? "status online"
                    : "status offline";

        document
            .getElementById(
                "homeQQ"
            )
            .textContent =
                qqOnline
                    ? "在线"
                    : "未登录";

        document
            .getElementById(
                "homeAI"
            )
            .textContent =
                aiReady
                    ? "就绪"
                    : "未启用";

        document
            .getElementById(
                "homeMemory"
            )
            .textContent =
                data.message_count || 0;

        document
            .getElementById(
                "statMessages"
            )
            .textContent =
                data.message_count || 0;

        document
            .getElementById(
                "statSent"
            )
            .textContent =
                data.sent_message_count || 0;

        document
            .getElementById(
                "statAI"
            )
            .textContent =
                data.ai_message_count || 0;

        setStatus(
            "statusQbot",
            data.running
        );

        setStatus(
            "statusQQ",
            qqOnline
        );

        setStatus(
            "statusAI",
            aiReady
        );

        document
            .getElementById(
                "topQQ"
            )
            .textContent =
                qqOnline
                    ? "●"
                    : "○";

    } catch (error) {

        console.error(error);
    }
}

/* ==========================================================
   Status helper
   ========================================================== */

function setStatus(
    id,
    online
) {

    const element =
        document.getElementById(id);

    if (!element) {
        return;
    }

    element.textContent =
        online
            ? "在线"
            : "离线";

    element.className =
        online
            ? "status online"
            : "status offline";
}

/* ==========================================================
   QQ
   ========================================================== */

async function loadQQ() {

    try {

        const data =
            await requestJSON(
                "/api/qq/status"
            );

        document
            .getElementById(
                "qqAccount"
            )
            .textContent =
                data.account || "-";

        document
            .getElementById(
                "qqNickname"
            )
            .textContent =
                data.nickname || "-";

        document
            .getElementById(
                "qqProvider"
            )
            .textContent =
                data.provider || "-";

        document
            .getElementById(
                "qqConnection"
            )
            .textContent =
                data.connected
                    ? "在线"
                    : "离线";

        const statusElement =
            document.getElementById(
                "qqPageStatus"
            );

        if (data.connected) {

            statusElement.textContent =
                "在线";

            statusElement.className =
                "status online";

        } else if (data.logged_in) {

            statusElement.textContent =
                "已登录";

            statusElement.className =
                "status login";

        } else {

            statusElement.textContent =
                "未登录";

            statusElement.className =
                "status offline";
        }

        const accountInput =
            document.getElementById(
                "qqLoginAccount"
            );

        if (
            accountInput &&
            !accountInput.value &&
            data.account
        ) {

            accountInput.value =
                data.account;
        }

        document
            .getElementById(
                "settingsQQ"
            )
            .textContent =
                data.connected
                    ? "在线"
                    : data.logged_in
                        ? "已登录"
                        : "未登录";

    } catch (error) {

        console.error(error);
    }
}

/* ==========================================================
   QQ Login
   ========================================================== */

async function qqLogin() {

    const result =
        document.getElementById(
            "qqLoginResult"
        );

    const account =
        document.getElementById(
            "qqLoginAccount"
        )
        .value
        .trim();

    if (!account) {

        result.textContent =
            "请输入 QQ 账号";

        return;
    }

    result.textContent =
        "正在登录 QQ...";

    try {

        const data =
            await requestJSON(
                "/api/qq/login",
                {

                    method: "POST",

                    headers: {
                        "Content-Type":
                            "application/json"
                    },

                    body:
                        JSON.stringify({
                            account:
                                account
                        })
                }
            );

        result.textContent =
            data.message ||
            "QQ 登录请求已发送";

        await loadQQ();

        await updateStatus();

    } catch (error) {

        result.textContent =
            "登录失败：" +
            error.message;
    }
}

/* ==========================================================
   QQ Logout
   ========================================================== */

async function qqLogout() {

    if (!confirm(
        "确定退出 QQ 登录吗？"
    )) {
        return;
    }

    const result =
        document.getElementById(
            "qqLoginResult"
        );

    result.textContent =
        "正在退出 QQ...";

    try {

        const data =
            await requestJSON(
                "/api/qq/logout",
                {
                    method: "POST"
                }
            );

        result.textContent =
            data.message ||
            "QQ 已退出";

        await loadQQ();

        await updateStatus();

    } catch (error) {

        result.textContent =
            "退出失败：" +
            error.message;
    }
}

/* ==========================================================
   QQ Start
   ========================================================== */

async function qqStart() {

    const result =
        document.getElementById(
            "qqLoginResult"
        );

    result.textContent =
        "正在启动 QQ Client...";

    try {

        const data =
            await requestJSON(
                "/api/qq/start",
                {
                    method: "POST"
                }
            );

        result.textContent =
            data.message ||
            "正在启动 QQ";

        setTimeout(
            function() {

                loadQQ();

                updateStatus();

            },
            500
        );

    } catch (error) {

        result.textContent =
            "启动失败：" +
            error.message;
    }
}

/* ==========================================================
   QQ Stop
   ========================================================== */

async function qqStop() {

    const result =
        document.getElementById(
            "qqLoginResult"
        );

    result.textContent =
        "正在停止 QQ...";

    try {

        const data =
            await requestJSON(
                "/api/qq/stop",
                {
                    method: "POST"
                }
            );

        result.textContent =
            data.message ||
            "QQ 已停止";

        setTimeout(
            function() {

                loadQQ();

                updateStatus();

            },
            300
        );

    } catch (error) {

        result.textContent =
            "停止失败：" +
            error.message;
    }
}

/* ==========================================================
   AI
   ========================================================== */

async function loadAI() {

    try {

        const data =
            await requestJSON(
                "/api/config"
            );

        const ai =
            data.ai;

        if (!ai) {
            return;
        }

        document
            .getElementById(
                "aiBaseURL"
            )
            .value =
                ai.base_url || "";

        document
            .getElementById(
                "aiAPIKey"
            )
            .value =
                ai.api_key || "";

        document
            .getElementById(
                "aiModel"
            )
            .value =
                ai.model || "";

        document
            .getElementById(
                "aiTemperature"
            )
            .value =
                ai.temperature ?? 0.7;

        document
            .getElementById(
                "aiMaxTokens"
            )
            .value =
                ai.max_tokens || 1200;

        document
            .getElementById(
                "aiSystemPrompt"
            )
            .value =
                ai.system_prompt || "";

        document
            .getElementById(
                "aiPageModel"
            )
            .textContent =
                ai.model || "-";

        setStatus(
            "aiPageStatus",
            ai.enabled
        );

    } catch (error) {

        console.error(error);
    }
}

/* ==========================================================
   Save AI
   ========================================================== */

async function saveAI() {

    const payload = {

        enabled: true,

        base_url:
            document
                .getElementById(
                    "aiBaseURL"
                )
                .value
                .trim(),

        api_key:
            document
                .getElementById(
                    "aiAPIKey"
                )
                .value
                .trim(),

        model:
            document
                .getElementById(
                    "aiModel"
                )
                .value
                .trim(),

        temperature:
            parseFloat(
                document
                    .getElementById(
                        "aiTemperature"
                    )
                    .value
            ),

        max_tokens:
            parseInt(
                document
                    .getElementById(
                        "aiMaxTokens"
                    )
                    .value,
                10
            ),

        system_prompt:
            document
                .getElementById(
                    "aiSystemPrompt"
                )
                .value
    };

    try {

        const result =
            await requestJSON(
                "/api/ai/save",
                {

                    method: "POST",

                    headers: {
                        "Content-Type":
                            "application/json"
                    },

                    body:
                        JSON.stringify(
                            payload
                        )
                }
            );

        document
            .getElementById(
                "aiResult"
            )
            .textContent =
                result.message ||
                "保存成功";

        updateStatus();

    } catch (error) {

        document
            .getElementById(
                "aiResult"
            )
            .textContent =
                "保存失败：" +
                error.message;
    }
}

/* ==========================================================
   Test AI
   ========================================================== */

async function testAI() {

    const resultElement =
        document.getElementById(
            "aiResult"
        );

    resultElement.textContent =
        "正在连接 AI...";

    try {

        const data =
            await requestJSON(
                "/api/ai/test",
                {

                    method: "POST",

                    headers: {
                        "Content-Type":
                            "application/json"
                    },

                    body:
                        JSON.stringify({
                            message:
                                "你好，请回复一句话测试 Qbot AI。"
                        })
                }
            );

        resultElement.textContent =
            data.reply ||
            "AI 返回为空";

        updateStatus();

    } catch (error) {

        resultElement.textContent =
            "测试失败：" +
            error.message;
    }
}

/* ==========================================================
   Memory
   ========================================================== */

async function loadMemory() {

    try {

        const data =
            await requestJSON(
                "/api/memory"
            );

        document
            .getElementById(
                "memoryStatus"
            )
            .textContent =
                data.enabled
                    ? "已启用"
                    : "已关闭";

        document
            .getElementById(
                "memoryCount"
            )
            .textContent =
                data.count || 0;

        document
            .getElementById(
                "memoryFile"
            )
            .textContent =
                data.file || "-";

        document
            .getElementById(
                "memoryContent"
            )
            .value =
                data.content || "";

    } catch (error) {

        console.error(error);
    }
}

async function clearMemory() {

    if (!confirm(
        "确定要清空全部 Memory 吗？"
    )) {
        return;
    }

    try {

        await requestJSON(
            "/api/memory/clear",
            {
                method: "POST"
            }
        );

        loadMemory();

    } catch (error) {

        alert(
            error.message
        );
    }
}

/* ==========================================================
   初始化
   ========================================================== */

updateStatus();

setInterval(
    updateStatus,
    3000
);

</script>

</body>

</html>`
}

// ============================================================
// boolText
// ============================================================

func boolText(
	value bool,
) string {

	if value {
		return "开启"
	}

	return "关闭"
}