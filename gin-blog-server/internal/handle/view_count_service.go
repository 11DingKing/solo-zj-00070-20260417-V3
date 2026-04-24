package handle

import (
	"context"
	"fmt"
	g "gin-blog/internal/global"
	"gin-blog/internal/model"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/go-redis/redis/v9"
	"gorm.io/gorm"
)

const (
	VIEW_COUNT_SYNC_THRESHOLD = 50              // 每累计 50 次同步一次
	VIEW_COUNT_SYNC_INTERVAL  = 10 * time.Minute // 每隔 10 分钟同步一次
	VIEW_COUNT_IP_EXPIRE      = 1 * time.Hour    // IP 防刷过期时间 1 小时
	REDIS_HEALTH_CHECK_TTL    = 5 * time.Second   // Redis 健康检查缓存时间
)

type ViewCountService struct {
	rdb               *redis.Client
	db                *gorm.DB
	lastSyncTime      time.Time
	mu                sync.Mutex
	redisHealthy      bool
	lastHealthCheck   time.Time
	healthCheckMu     sync.RWMutex
	fallbackIpCache   sync.Map
}

var viewCountService *ViewCountService
var viewCountServiceOnce sync.Once

func GetViewCountService(rdb *redis.Client, db *gorm.DB) *ViewCountService {
	viewCountServiceOnce.Do(func() {
		viewCountService = &ViewCountService{
			rdb:             rdb,
			db:              db,
			lastSyncTime:    time.Now(),
			redisHealthy:    true,
			lastHealthCheck: time.Time{},
		}
	})
	return viewCountService
}

func (s *ViewCountService) getArticleViewCountKey(articleId int) string {
	return g.VIEW_COUNT_ARTICLE_PREFIX + strconv.Itoa(articleId)
}

func (s *ViewCountService) getIpViewCountKey(articleId int, ip string) string {
	return fmt.Sprintf("%s%d:%s", g.VIEW_COUNT_IP_PREFIX, articleId, ip)
}

func (s *ViewCountService) getFallbackIpCacheKey(articleId int, ip string) string {
	return fmt.Sprintf("%d:%s", articleId, ip)
}

func (s *ViewCountService) checkRedisHealth(ctx context.Context) bool {
	s.healthCheckMu.RLock()
	if time.Since(s.lastHealthCheck) < REDIS_HEALTH_CHECK_TTL {
		result := s.redisHealthy
		s.healthCheckMu.RUnlock()
		return result
	}
	s.healthCheckMu.RUnlock()

	s.healthCheckMu.Lock()
	defer s.healthCheckMu.Unlock()

	if time.Since(s.lastHealthCheck) < REDIS_HEALTH_CHECK_TTL {
		return s.redisHealthy
	}

	if s.rdb == nil {
		s.redisHealthy = false
		s.lastHealthCheck = time.Now()
		return false
	}

	_, err := s.rdb.Ping(ctx).Result()
	s.redisHealthy = err == nil
	s.lastHealthCheck = time.Now()

	if !s.redisHealthy {
		slog.Warn("ViewCountService: Redis is unhealthy", "err", err)
	}

	return s.redisHealthy
}

func (s *ViewCountService) isIpAlreadyViewed(ctx context.Context, articleId int, ip string) bool {
	if s.checkRedisHealth(ctx) {
		ipKey := s.getIpViewCountKey(articleId, ip)
		exists, err := s.rdb.Exists(ctx, ipKey).Result()
		if err == nil {
			return exists > 0
		}
		slog.Warn("ViewCountService: Redis exists check failed, using fallback", "err", err)
	}

	cacheKey := s.getFallbackIpCacheKey(articleId, ip)
	if val, ok := s.fallbackIpCache.Load(cacheKey); ok {
		expireTime := val.(time.Time)
		if time.Now().Before(expireTime) {
			return true
		}
		s.fallbackIpCache.Delete(cacheKey)
	}
	return false
}

func (s *ViewCountService) recordIpView(ctx context.Context, articleId int, ip string) {
	if s.checkRedisHealth(ctx) {
		ipKey := s.getIpViewCountKey(articleId, ip)
		err := s.rdb.SetNX(ctx, ipKey, 1, VIEW_COUNT_IP_EXPIRE).Err()
		if err == nil {
			return
		}
		slog.Warn("ViewCountService: Redis set IP record failed, using fallback", "err", err)
	}

	cacheKey := s.getFallbackIpCacheKey(articleId, ip)
	expireTime := time.Now().Add(VIEW_COUNT_IP_EXPIRE)
	s.fallbackIpCache.Store(cacheKey, expireTime)
}

func (s *ViewCountService) IncrementViewCount(ctx context.Context, articleId int, ip string) (shouldSync bool, err error) {
	if s.isIpAlreadyViewed(ctx, articleId, ip) {
		return false, nil
	}

	s.recordIpView(ctx, articleId, ip)

	if s.checkRedisHealth(ctx) {
		articleKey := s.getArticleViewCountKey(articleId)

		pipe := s.rdb.Pipeline()
		viewCount := pipe.Incr(ctx, articleKey)
		pipe.SAdd(ctx, g.VIEW_COUNT_SYNC_KEY, strconv.Itoa(articleId))

		_, err = pipe.Exec(ctx)
		if err != nil {
			slog.Error("ViewCountService.IncrementViewCount pipeline error, falling back to MySQL", "err", err)
		} else {
			currentCount := viewCount.Val()
			shouldSync = currentCount%VIEW_COUNT_SYNC_THRESHOLD == 0
			return shouldSync, nil
		}
	}

	slog.Info("ViewCountService: Falling back to direct MySQL update for view count", "articleId", articleId)
	result := s.db.Model(&model.Article{}).Where("id = ?", articleId).
		UpdateColumn("view_count", gorm.Expr("view_count + 1"))
	
	if result.Error != nil {
		slog.Error("ViewCountService: Failed to update view count in MySQL", "err", result.Error)
		return false, result.Error
	}

	return false, nil
}

func (s *ViewCountService) GetViewCount(ctx context.Context, articleId int) int64 {
	var redisCount int64 = 0

	if s.checkRedisHealth(ctx) {
		articleKey := s.getArticleViewCountKey(articleId)
		count, err := s.rdb.Get(ctx, articleKey).Int64()
		if err == nil {
			redisCount = count
		} else if err != redis.Nil {
			slog.Error("ViewCountService.GetViewCount redis error", "err", err)
		}
	}

	var article model.Article
	if err := s.db.Model(&model.Article{}).Select("view_count").Where("id = ?", articleId).First(&article).Error; err == nil {
		return int64(article.ViewCount) + redisCount
	}

	return redisCount
}

func (s *ViewCountService) GetBatchViewCount(ctx context.Context, articleIds []int) map[int]int64 {
	result := make(map[int]int64)

	if len(articleIds) == 0 {
		return result
	}

	for _, id := range articleIds {
		result[id] = 0
	}

	if s.checkRedisHealth(ctx) {
		pipe := s.rdb.Pipeline()
		cmds := make(map[int]*redis.StringCmd)

		for _, id := range articleIds {
			cmds[id] = pipe.Get(ctx, s.getArticleViewCountKey(id))
		}

		_, err := pipe.Exec(ctx)
		if err != nil && err != redis.Nil {
			slog.Error("ViewCountService.GetBatchViewCount pipeline error", "err", err)
		} else {
			for id, cmd := range cmds {
				if count, err := cmd.Int64(); err == nil {
					result[id] = count
				}
			}
		}
	}

	var dbArticles []model.Article
	if err := s.db.Model(&model.Article{}).Select("id, view_count").Where("id IN ?", articleIds).Find(&dbArticles).Error; err == nil {
		for _, article := range dbArticles {
			result[article.ID] += int64(article.ViewCount)
		}
	}

	return result
}

func (s *ViewCountService) SyncToDatabase(ctx context.Context) error {
	if !s.checkRedisHealth(ctx) {
		slog.Warn("ViewCountService.SyncToDatabase: Redis is unhealthy, skipping sync")
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	articleIdsStr, err := s.rdb.SMembers(ctx, g.VIEW_COUNT_SYNC_KEY).Result()
	if err != nil {
		if err != redis.Nil {
			slog.Error("ViewCountService.SyncToDatabase SMembers error", "err", err)
		}
		return err
	}

	if len(articleIdsStr) == 0 {
		return nil
	}

	pipe := s.rdb.Pipeline()
	cmds := make(map[int]*redis.StringCmd)

	for _, idStr := range articleIdsStr {
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		cmds[id] = pipe.Get(ctx, s.getArticleViewCountKey(id))
	}

	_, err = pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		slog.Error("ViewCountService.SyncToDatabase pipeline error", "err", err)
		return err
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		for id, cmd := range cmds {
			redisCount, err := cmd.Int64()
			if err != nil {
				if err != redis.Nil {
					slog.Error("ViewCountService.SyncToDatabase get count error", "id", id, "err", err)
				}
				continue
			}

			if redisCount <= 0 {
				continue
			}

			result := tx.Model(&model.Article{}).Where("id = ?", id).
				UpdateColumn("view_count", gorm.Expr("view_count + ?", redisCount))
			if result.Error != nil {
				slog.Error("ViewCountService.SyncToDatabase update error", "id", id, "err", result.Error)
				return result.Error
			}

			if result.RowsAffected > 0 {
				s.rdb.DecrBy(ctx, s.getArticleViewCountKey(id), redisCount)
			}
		}
		return nil
	})

	if err != nil {
		return err
	}

	s.rdb.Del(ctx, g.VIEW_COUNT_SYNC_KEY)
	s.lastSyncTime = time.Now()

	slog.Info("ViewCountService.SyncToDatabase completed", "count", len(cmds))
	return nil
}

func (s *ViewCountService) ShouldSyncByTime() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastSyncTime) >= VIEW_COUNT_SYNC_INTERVAL
}

func StartViewCountSyncJob(rdb *redis.Client, db *gorm.DB) {
	service := GetViewCountService(rdb, db)
	ticker := time.NewTicker(VIEW_COUNT_SYNC_INTERVAL)

	go func() {
		for range ticker.C {
			ctx := context.Background()
			if err := service.SyncToDatabase(ctx); err != nil {
				slog.Error("ViewCountSyncJob error", "err", err)
			}
		}
	}()

	slog.Info("ViewCountSyncJob started", "interval", VIEW_COUNT_SYNC_INTERVAL)
}
