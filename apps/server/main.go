package main

import (
	"log"
	"os"

	"github.com/cloudwego/hertz/pkg/app/server"
	httpapi "github.com/sorrymaker-01/lmy-harness/apps/server/internal/http"
)

// maxRequestBodySize 定义 HTTP 请求体的最大字节数（320MB）。
// 之所以设置得这么大，是为了支持知识库文件上传等大体积请求
// （例如批量导入文档到 RAG 知识库），避免 Hertz 默认的 4MB 限制导致上传失败。
const maxRequestBodySize = 320 << 20

// main 是整个 Agent 服务的进程入口，负责：
//  1. 从环境变量读取监听地址（ADDR，默认 127.0.0.1:3000）
//     和前端静态资源目录（WEB_DIST_DIR，默认 apps/web/dist）；
//  2. 构造 HTTPServer——这一步会完成全部核心组件的装配：
//     加载启动上下文（CLAUDE.md/.mcp.json/skills 等）、打开 SQLite 状态库、
//     初始化记忆存储、工具运行时（含 MCP 工具注册）、技能注册表、
//     知识库存储以及 Agent 本体（详见 internal/http/server.go 的 NewHTTPServer）；
//  3. 创建 CloudWeGo Hertz 服务器并注册全部路由（REST API + SSE + 静态资源）；
//  4. 调用 h.Spin() 阻塞运行，直至进程退出。
func main() {
	addr := getenv("ADDR", "127.0.0.1:3000")
	staticDir := getenv("WEB_DIST_DIR", "apps/web/dist")
	// NewHTTPServer 内部完成所有组件的初始化与依赖装配，
	// 是真正的"组装根"（composition root）；main 本身保持极薄。
	httpServer := httpapi.NewHTTPServer(staticDir)
	h := server.Default(
		server.WithHostPorts(addr),
		server.WithMaxRequestBodySize(maxRequestBodySize),
	)
	// 将业务路由（会话、消息、模型配置、知识库、技能、MCP 管理等）
	// 挂载到 Hertz 引擎上。
	httpServer.Register(h)
	log.Printf("Lmy' Harness Agent server listening on http://%s", addr)
	log.Printf("Serving frontend from %s", staticDir)
	// Spin 启动事件循环并阻塞当前 goroutine，同时处理优雅退出信号。
	h.Spin()
}

// getenv 读取环境变量 key，若为空则返回 fallback 默认值。
// 用于让部署方通过环境变量覆盖监听地址与静态目录，而无需改代码。
func getenv(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
