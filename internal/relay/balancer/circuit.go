package balancer

import (
	"fmt"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/log"
)

// CircuitState 熔断器状态
type CircuitState int

const (
	StateClosed   CircuitState = iota // 正常通行
	StateOpen                         // 熔断中，拒绝所有请求
	StateHalfOpen                     // 半开，仅允许单个试探请求
)

// circuitEntry 单个熔断器条目
type circuitEntry struct {
	State               CircuitState
	ConsecutiveFailures int64
	LastFailureTime     time.Time
	TripCount           int // 累计熔断触发次数（用于指数退避）
	mu                  sync.Mutex
}

// 全局熔断器存储
var globalBreaker sync.Map // key: string -> value: *circuitEntry

// atomic 缓存的熔断器配置（避免高并发时频繁查询设置）
var (
	atomicThreshold    atomic.Int64 // 熔断阈值，默认 5
	atomicBaseCooldown atomic.Int64 // 基础冷却时间(秒)，默认 60
	atomicMaxCooldown  atomic.Int64 // 最大冷却时间(秒)，默认 600
)

// circuitKey 生成熔断器键：channelID:channelKeyID:modelName
func circuitKey(channelID, keyID int, modelName string) string {
	return fmt.Sprintf("%d:%d:%s", channelID, keyID, modelName)
}

// getOrCreateEntry 获取或创建熔断器条目
func getOrCreateEntry(key string) *circuitEntry {
	if v, ok := globalBreaker.Load(key); ok {
		return v.(*circuitEntry)
	}
	entry := &circuitEntry{State: StateClosed}
	actual, _ := globalBreaker.LoadOrStore(key, entry)
	return actual.(*circuitEntry)
}

// init 初始化熔断器配置的默认值，并启动定期 GC。
func init() {
	atomicThreshold.Store(5)
	atomicBaseCooldown.Store(60)
	atomicMaxCooldown.Store(600)

	go circuitGC(10 * time.Minute)
}

// circuitGC periodically cleans up stale circuit breaker entries.
// Entries that have been in Closed state with no failures for over 1 hour are removed.
func circuitGC(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		var deletedCount int
		now := time.Now()
		globalBreaker.Range(func(key, value interface{}) bool {
			entry := value.(*circuitEntry)
			entry.mu.Lock()
			// Remove if Closed and last failure was over 1 hour ago
			// This indicates the channel is healthy and the entry can be safely removed
			canDelete := entry.State == StateClosed && now.Sub(entry.LastFailureTime) > time.Hour
			entry.mu.Unlock()
			if canDelete {
				globalBreaker.Delete(key)
				deletedCount++
			}
			return true
		})
		if deletedCount > 0 {
			log.Debugf("circuit breaker GC: cleaned up %d stale entries", deletedCount)
		}
	}
}

// RefreshCircuitConfig 从设置中刷新熔断器配置
// 由外部调用（如设置变更时）来更新 atomic 缓存值
func RefreshCircuitConfig() {
	if v, err := op.SettingGetInt(model.SettingKeyCircuitBreakerThreshold); err == nil && v > 0 {
		atomicThreshold.Store(int64(v))
	} else {
		atomicThreshold.Store(5)
	}

	if v, err := op.SettingGetInt(model.SettingKeyCircuitBreakerCooldown); err == nil && v > 0 {
		atomicBaseCooldown.Store(int64(v))
	} else {
		atomicBaseCooldown.Store(60)
	}

	if v, err := op.SettingGetInt(model.SettingKeyCircuitBreakerMaxCooldown); err == nil && v > 0 {
		atomicMaxCooldown.Store(int64(v))
	} else {
		atomicMaxCooldown.Store(600)
	}
}

// getThreshold 获取熔断阈值配置（直接读取 atomic 值）
func getThreshold() int64 {
	return atomicThreshold.Load()
}

// GetCooldown 获取当前冷却时间（带指数退避，直接读取 atomic 值）
func GetCooldown(tripCount int) time.Duration {
	base := int(atomicBaseCooldown.Load())
	maxCooldown := int(atomicMaxCooldown.Load())

	// 指数退避：baseCooldown * 2^(tripCount-1)
	cooldown := base
	if tripCount > 1 {
		shift := tripCount - 1
		if shift > 20 { // 防止溢出
			shift = 20
		}
		cooldown = base << shift
	}
	if cooldown > maxCooldown {
		cooldown = maxCooldown
	}

	return time.Duration(cooldown) * time.Second
}

// IsTripped 检查通道是否处于熔断状态
// 返回 tripped=true 表示该通道应被跳过，remaining 为剩余冷却时间
func IsTripped(channelID, keyID int, modelName string) (tripped bool, remaining time.Duration) {
	key := circuitKey(channelID, keyID, modelName)
	v, ok := globalBreaker.Load(key)
	if !ok {
		return false, 0 // 无记录，视为 Closed
	}
	entry := v.(*circuitEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	switch entry.State {
	case StateClosed:
		return false, 0

	case StateOpen:
		cooldown := GetCooldown(entry.TripCount)
		elapsed := time.Since(entry.LastFailureTime)
		if elapsed >= cooldown {
			entry.State = StateHalfOpen
			log.Infof("circuit breaker [%s] Open -> HalfOpen (cooldown %v elapsed)", key, cooldown)
			return false, 0
		}
		// 仍在冷却中
		return true, cooldown - elapsed

	case StateHalfOpen:
		// 已有试探请求在进行中，拒绝其他请求
		return true, 0

	default:
		return false, 0
	}
}

// RecordSuccess 记录成功，重置熔断器状态
func RecordSuccess(channelID, keyID int, modelName string) {
	key := circuitKey(channelID, keyID, modelName)
	v, ok := globalBreaker.Load(key)
	if !ok {
		return
	}
	entry := v.(*circuitEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	if entry.State == StateHalfOpen {
		log.Infof("circuit breaker [%s] HalfOpen -> Closed (probe succeeded)", key)
	}

	// 重置全部状态
	entry.State = StateClosed
	entry.ConsecutiveFailures = 0
	entry.TripCount = 0
}

// RecordFailure 记录失败，可能触发熔断
func RecordFailure(channelID, keyID int, modelName string) {
	key := circuitKey(channelID, keyID, modelName)
	entry := getOrCreateEntry(key)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	entry.LastFailureTime = time.Now()

	switch entry.State {
	case StateClosed:
		entry.ConsecutiveFailures++
		threshold := getThreshold()
		if entry.ConsecutiveFailures >= threshold {
			entry.State = StateOpen
			entry.TripCount++
			log.Warnf("circuit breaker [%s] Closed -> Open (failures=%d >= threshold=%d, tripCount=%d, cooldown=%v)",
				key, entry.ConsecutiveFailures, threshold, entry.TripCount, GetCooldown(entry.TripCount))
		}

	case StateHalfOpen:
		// 试探失败，重新进入 Open 状态，TripCount 递增（冷却时间翻倍）
		entry.State = StateOpen
		entry.TripCount++
		entry.ConsecutiveFailures = 0 // 重新开始计数
		log.Warnf("circuit breaker [%s] HalfOpen -> Open (probe failed, tripCount=%d, cooldown=%v)",
			key, entry.TripCount, GetCooldown(entry.TripCount))

	case StateOpen:
		// 理论上不应该在 Open 状态下接收到失败记录（请求应被拒绝），
		// 但为安全起见仍更新失败时间
	}
}
