package op

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/snowflake"
)

const relayLogMaxSize = 20
const relayLogMaxSizeNoDB = 100   // 当不保存到数据库时，允许更大的缓存用于实时查询
const relayLogMaxPendingDB = 1000 // DB 异步写入跟不上时的内存保护上限
const relayLogFlushTimeout = 30 * time.Second

var relayLogCache = make([]model.RelayLog, 0, relayLogMaxSize)
var relayLogCacheLock sync.Mutex

var relayLogFlushLock sync.Mutex
var relayLogFlushSignal = make(chan struct{}, 1)

var relayLogSubscribers = make(map[chan model.RelayLog]struct{})
var relayLogSubscribersLock sync.RWMutex

var relayLogStreamTokens = make(map[string]struct{})
var relayLogStreamTokensLock sync.RWMutex

// atomicRelayLogEnabled caches the relay log enabled setting to avoid frequent SettingGetBool calls.
var atomicRelayLogEnabled atomic.Bool

// init initializes the atomic cache with default value.
func init() {
	atomicRelayLogEnabled.Store(true) // default to enabled
	go relayLogFlushWorker()
}

func relayLogFlushWorker() {
	for range relayLogFlushSignal {
		ctx, cancel := context.WithTimeout(context.Background(), relayLogFlushTimeout)
		if err := relayLogFlushToDB(ctx); err != nil {
			log.Warnf("relay log async flush failed: %v", err)
		}
		cancel()
	}
}

func signalRelayLogFlush() {
	select {
	case relayLogFlushSignal <- struct{}{}:
	default:
	}
}

// RefreshRelayLogConfig refreshes the atomic cache for relay log enabled setting.
// Should be called when the setting is changed.
func RefreshRelayLogConfig() {
	enabledStr, err := SettingGetString(model.SettingKeyRelayLogKeepEnabled)
	if err != nil {
		atomicRelayLogEnabled.Store(true) // default to enabled
		return
	}
	enabled, err := strconv.ParseBool(enabledStr)
	if err != nil {
		atomicRelayLogEnabled.Store(true) // default to enabled
		return
	}
	atomicRelayLogEnabled.Store(enabled)
}

func RelayLogStreamTokenCreate() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(bytes)

	relayLogStreamTokensLock.Lock()
	relayLogStreamTokens[token] = struct{}{}
	relayLogStreamTokensLock.Unlock()

	return token, nil
}

func RelayLogStreamTokenVerify(token string) bool {
	relayLogStreamTokensLock.RLock()
	_, ok := relayLogStreamTokens[token]
	relayLogStreamTokensLock.RUnlock()
	return ok
}

func RelayLogStreamTokenRevoke(token string) {
	relayLogStreamTokensLock.Lock()
	delete(relayLogStreamTokens, token)
	relayLogStreamTokensLock.Unlock()
}

func RelayLogSubscribe() chan model.RelayLog {
	ch := make(chan model.RelayLog, 10)
	relayLogSubscribersLock.Lock()
	relayLogSubscribers[ch] = struct{}{}
	relayLogSubscribersLock.Unlock()
	return ch
}

func RelayLogUnsubscribe(ch chan model.RelayLog) {
	relayLogSubscribersLock.Lock()
	delete(relayLogSubscribers, ch)
	relayLogSubscribersLock.Unlock()
	close(ch)
}

func notifySubscribers(relayLog model.RelayLog) {
	relayLogSubscribersLock.RLock()
	defer relayLogSubscribersLock.RUnlock()

	for ch := range relayLogSubscribers {
		select {
		case ch <- relayLog:
		default:
		}
	}
}

func relayLogFlushToDB(ctx context.Context) error {
	relayLogFlushLock.Lock()
	defer relayLogFlushLock.Unlock()

	relayLogCacheLock.Lock()
	if len(relayLogCache) == 0 {
		relayLogCacheLock.Unlock()
		return nil
	}
	batch := make([]model.RelayLog, len(relayLogCache))
	copy(batch, relayLogCache)
	relayLogCacheLock.Unlock()

	result := db.GetDB().WithContext(ctx).Create(&batch)
	if result.Error != nil {
		return result.Error
	}

	flushedIDs := make(map[int64]struct{}, len(batch))
	for _, item := range batch {
		flushedIDs[item.ID] = struct{}{}
	}

	relayLogCacheLock.Lock()
	if len(relayLogCache) > 0 {
		kept := relayLogCache[:0]
		for _, item := range relayLogCache {
			if _, flushed := flushedIDs[item.ID]; !flushed {
				kept = append(kept, item)
			}
		}
		relayLogCache = kept
	}
	if len(relayLogCache) == 0 {
		relayLogCache = make([]model.RelayLog, 0, relayLogMaxSize)
	}
	relayLogCacheLock.Unlock()

	return nil
}

func RelayLogAdd(_ context.Context, relayLog model.RelayLog) error {
	enabled := atomicRelayLogEnabled.Load()
	maxSize := relayLogMaxSize
	if !enabled {
		maxSize = relayLogMaxSizeNoDB
	}
	relayLog.ID = snowflake.GenerateID()
	notifySubscribers(relayLog)

	shouldFlush := false
	dropped := 0
	relayLogCacheLock.Lock()
	relayLogCache = append(relayLogCache, relayLog)
	if len(relayLogCache) >= maxSize {
		if enabled {
			shouldFlush = true
			if len(relayLogCache) > relayLogMaxPendingDB {
				dropped = len(relayLogCache) - relayLogMaxPendingDB
				relayLogCache = relayLogCache[dropped:]
			}
		} else {
			// 如果未启用日志保存，移除最旧的日志，保留最新的日志用于实时查询
			keepSize := maxSize / 2
			if len(relayLogCache) > keepSize {
				relayLogCache = relayLogCache[len(relayLogCache)-keepSize:]
			}
		}
	}
	relayLogCacheLock.Unlock()
	if dropped > 0 {
		log.Warnf("relay log pending cache exceeded %d, dropped %d oldest entries", relayLogMaxPendingDB, dropped)
	}
	if shouldFlush {
		signalRelayLogFlush()
	}
	return nil
}

func RelayLogSaveDBTask(ctx context.Context) error {
	log.Debugf("relay log save db task started")
	startTime := time.Now()
	defer func() {
		log.Debugf("relay log save db task finished, save time: %s", time.Since(startTime))
	}()
	enabled, err := SettingGetBool(model.SettingKeyRelayLogKeepEnabled)
	if err != nil {
		return err
	}

	if enabled {
		if err := relayLogFlushToDB(ctx); err != nil {
			return err
		}
		return relayLogCleanup(ctx)
	}

	// 如果未启用日志保存，检查缓存大小，如果超过限制则清理旧日志
	relayLogCacheLock.Lock()
	if len(relayLogCache) > relayLogMaxSizeNoDB {
		keepSize := relayLogMaxSizeNoDB / 2
		relayLogCache = relayLogCache[len(relayLogCache)-keepSize:]
	}
	relayLogCacheLock.Unlock()

	return nil
}

func relayLogCleanup(ctx context.Context) error {
	keepPeriod, err := SettingGetInt(model.SettingKeyRelayLogKeepPeriod)
	if err != nil {
		return err
	}

	if keepPeriod <= 0 {
		return nil
	}

	cutoffTime := time.Now().Add(-time.Duration(keepPeriod) * 24 * time.Hour).Unix()
	batchSize := 1000

	// Batch delete to avoid long write locks that block other operations
	for {
		result := db.GetDB().WithContext(ctx).Where("time < ?", cutoffTime).Limit(batchSize).Delete(&model.RelayLog{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected < int64(batchSize) {
			break // All expired logs deleted
		}
	}

	// Run incremental vacuum to reclaim disk space from deleted rows
	// This is a no-op for non-SQLite databases
	if db.GetDB().Dialector.Name() == "sqlite" {
		db.GetDB().Exec("PRAGMA incremental_vacuum(1000)")
	}
	return nil
}

// RelayLogList 查询日志列表，支持可选的时间范围过滤
// startTime 和 endTime 为 nil 时表示不限制时间范围
func RelayLogList(ctx context.Context, startTime, endTime *int, page, pageSize int) ([]model.RelayLog, error) {
	enabled, err := SettingGetBool(model.SettingKeyRelayLogKeepEnabled)
	if err != nil {
		return nil, err
	}
	hasTimeFilter := startTime != nil && endTime != nil

	// 获取缓存中符合条件的日志
	relayLogCacheLock.Lock()
	var cachedLogs []model.RelayLog
	for _, log := range relayLogCache {
		if hasTimeFilter {
			if log.Time >= int64(*startTime) && log.Time <= int64(*endTime) {
				cachedLogs = append(cachedLogs, log)
			}
		} else {
			cachedLogs = append(cachedLogs, log)
		}
	}
	relayLogCacheLock.Unlock()

	// 反转缓存日志顺序（原本新的在末尾，反转后新的在前面，方便分页）
	for i, j := 0, len(cachedLogs)-1; i < j; i, j = i+1, j-1 {
		cachedLogs[i], cachedLogs[j] = cachedLogs[j], cachedLogs[i]
	}

	cacheCount := len(cachedLogs)
	offset := (page - 1) * pageSize

	var result []model.RelayLog

	// 先从缓存中取（缓存是最新的日志）
	if offset < cacheCount {
		cacheEnd := offset + pageSize
		if cacheEnd > cacheCount {
			cacheEnd = cacheCount
		}
		result = append(result, cachedLogs[offset:cacheEnd]...)
	}

	// 如果启用了日志保存，缓存不够时从数据库补充
	if enabled {
		remaining := pageSize - len(result)
		if remaining > 0 {
			dbOffset := 0
			if offset > cacheCount {
				dbOffset = offset - cacheCount
			}

			query := db.GetDB().WithContext(ctx)
			if hasTimeFilter {
				query = query.Where("time >= ? AND time <= ?", *startTime, *endTime)
			}

			var dbLogs []model.RelayLog
			if err := query.Order("id DESC").Offset(dbOffset).Limit(remaining).Find(&dbLogs).Error; err != nil {
				return nil, err
			}
			result = append(result, dbLogs...)
		}
	}

	return result, nil
}

func RelayLogClear(ctx context.Context) error {
	relayLogCacheLock.Lock()
	relayLogCache = make([]model.RelayLog, 0, relayLogMaxSize)
	relayLogCacheLock.Unlock()
	return db.GetDB().WithContext(ctx).Where("1 = 1").Delete(&model.RelayLog{}).Error
}
