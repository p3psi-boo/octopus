package relay

import (
	"os"
	"strconv"
	"strings"

	"github.com/bestruirui/octopus/internal/conf"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/transformer/model"
	"github.com/gin-gonic/gin"
)

// maxSSEEventSize 定义 SSE 事件的最大大小。
// 对于图像生成模型（如 gemini-3-pro-image-preview），返回的 base64 编码图像数据
// 可能非常大（高分辨率图像可能超过 10MB），因此需要设置足够大的缓冲区。
// 默认 32MB，可通过环境变量 OCTOPUS_RELAY_MAX_SSE_EVENT_SIZE 覆盖。
var maxSSEEventSize = 32 * 1024 * 1024

func init() {
	if raw := strings.TrimSpace(os.Getenv(strings.ToUpper(conf.APP_NAME) + "_RELAY_MAX_SSE_EVENT_SIZE")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			maxSSEEventSize = v
		}
	}
}

// hopByHopHeaders 定义不应转发的 HTTP 头
// 使用 canonical key 格式（首字母大写，连字符分隔），避免 copyHeaders 中的 strings.ToLower
var hopByHopHeaders = map[string]bool{
	"Authorization":       true,
	"X-Api-Key":           true,
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
	"Content-Length":      true,
	"Host":                true,
	"Accept-Encoding":     true,
	"X-Forwarded-For":     true,
	"X-Forwarded-Host":    true,
	"X-Forwarded-Proto":   true,
	"X-Forwarded-Port":    true,
	"X-Real-Ip":           true,
	"Forwarded":           true,
	"Cf-Connecting-Ip":    true,
	"True-Client-Ip":      true,
	"X-Client-Ip":         true,
	"X-Cluster-Client-Ip": true,
}

type relayRequest struct {
	c               *gin.Context
	inAdapter       model.Inbound
	internalRequest *model.InternalLLMRequest
	metrics         *RelayMetrics
	apiKeyID        int
	requestModel    string
	iter            *balancer.Iterator
}

// relayAttempt 尝试级上下文
type relayAttempt struct {
	*relayRequest // 嵌入请求级上下文

	outAdapter           model.Outbound
	channel              *dbmodel.Channel
	usedKey              dbmodel.ChannelKey
	firstTokenTimeOutSec int
}

// attemptResult 封装单次尝试的结果
type attemptResult struct {
	Success bool  // 是否成功
	Written bool  // 流式响应是否已开始写入（不可重试）
	Err     error // 失败时的错误
}
