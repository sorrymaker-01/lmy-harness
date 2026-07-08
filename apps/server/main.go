package main

import (
	"log"
	"os"

	"github.com/cloudwego/hertz/pkg/app/server"
	httpapi "github.com/sorrymaker-01/lmy-harness/apps/server/internal/http"
)

const maxRequestBodySize = 320 << 20

func main() {
	addr := getenv("ADDR", "127.0.0.1:3000")
	staticDir := getenv("WEB_DIST_DIR", "apps/web/dist")
	httpServer := httpapi.NewHTTPServer(staticDir)
	h := server.Default(
		server.WithHostPorts(addr),
		server.WithMaxRequestBodySize(maxRequestBodySize),
	)
	httpServer.Register(h)
	log.Printf("Lmy' Harness Agent server listening on http://%s", addr)
	log.Printf("Serving frontend from %s", staticDir)
	h.Spin()
}

func getenv(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
