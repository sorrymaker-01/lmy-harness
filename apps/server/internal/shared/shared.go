package shared

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Now 返回当前的 UTC 时间。
// 全项目统一通过该函数取时间，保证持久化到 SQLite 及 JSON 序列化时
// 时区一致（均为 UTC），避免跨时区部署时时间戳混乱。
func Now() time.Time {
	return time.Now().UTC()
}

// NewID 生成一个带业务前缀的随机 ID，格式为 "<prefix>_<24位十六进制>"。
// 实现方式：读取 12 字节加密安全随机数（crypto/rand）后做 hex 编码；
// 若随机源意外失败（几乎不会发生），则退化为使用纳秒级时间戳的 hex 编码，
// 以保证函数永不返回空且仍具备足够的唯一性。
// 典型用法：消息 ID（msg_xxx）、tool call ID（call_xxx）、trace ID 等。
func NewID(prefix string) string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return prefix + "_" + hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}

// CompactJSON 将任意值序列化为紧凑 JSON 字符串，并截断到最多 max 个字符。
// 若 JSON 序列化失败，则退化为 fmt.Sprint 的字符串表示再截断。
// 主要用于把工具调用的输入/输出压缩成简短摘要，写入日志、trace
// 或工作记忆（working memory），防止超长内容撑爆上下文。
func CompactJSON(value any, max int) string {
	bytes, err := json.Marshal(value)
	if err != nil {
		return TrimRunes(fmt.Sprint(value), max)
	}
	return TrimRunes(string(bytes), max)
}

// TrimRunes 按"字符（rune）"而非字节截断字符串到最多 max 个字符，
// 超长时以 "..." 结尾。使用 rune 切片是为了避免把多字节 UTF-8
// 字符（如中文）从中间截断产生乱码。max <= 0 表示不限制。
func TrimRunes(value string, max int) string {
	runes := []rune(value)
	if max <= 0 || len(runes) <= max {
		return value
	}
	return string(runes[:max-1]) + "..."
}
