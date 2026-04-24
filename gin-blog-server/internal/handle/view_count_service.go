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
)

type ViewCountService struct {
	rdb          *redis.Client
	db           *gorm.DB
	lastSyncTime time.Time
	mu           sync.Mutex
}

var viewCountService *ViewCountService
var viewCountServiceOnce sync.Once

func GetViewCountService(rdb *redis.Client, db *gorm.DB) *ViewCountService {
	viewCountServiceOnce.Do(func() {
		viewCountService = &ViewCountService{
			rdb:          rdb,
			db:           db,
			lastSyncTime: time.Now(),
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

func (s *ViewCountService) IncrementViewCount(ctx context.Context, articleId int, ip string) (shouldSync bool, err error) {
	if s.rdb == nil {
		return false, nil
	}

	ipKey := s.getIpViewCountKey(articleId, ip)
	articleKey := s.getArticleViewCountKey(articleId)

	pipe := s.rdb.Pipeline()

	ipExists := pipe.Exists(ctx, ipKey)
	pipe.SetNX(ctx, ipKey, 1, VIEW_COUNT_IP_EXPIRE)
	viewCount := pipe.Incr(ctx, articleKey)
	pipe.SAdd(ctx, g.VIEW_COUNT_SYNC_KEY, strconv.Itoa(articleId))

	_, err = pipe.Exec(ctx)
	if err != nil {
		slog.Error("ViewCountService.IncrementViewCount pipeline error", "err", err)
		return false, err
	}

	if ipExists.Val() > 0 {
		return false, nil
	}

	currentCount := viewCount.Val()
	shouldSync = currentCount%VIEW_COUNT_SYNC_THRESHOLD == 0

	return shouldSync, nil
}

func (s *ViewCountService) GetViewCount(ctx context.Context, articleId int) int64 {
	if s.rdb == nil {
		var article model.Article
		if err := s.db.Model(&model.Article{}).Select("view_count").Where("id = ?", articleId).First(&article).Error; err == nil {
			return int64(article.ViewCount)
		}
		return 0
	}

	count, err := s.rdb.Get(ctx, s.getArticleViewCountKey(articleId)).Int64()
	if err != nil {
		if err != redis.Nil {
			slog.Error("ViewCountService.GetViewCount error", "err", err)
		}
		var article model.Article
		if err := s.db.Model(&model.Article{}).Select("view_count").Where("id = ?", articleId).First(&article).Error; err == nil {
			return int64(article.ViewCount)
		}
		return 0
	}

	return count
}

func (s *ViewCountService) GetBatchViewCount(ctx context.Context, articleIds []int) map[int]int64 {
	result := make(map[int]int64)

	if len(articleIds) == 0 {
		return result
	}

	for _, id := range articleIds {
		result[id] = 0
	}

	if s.rdb != nil {
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
	idsToQuery := make([]int, 0)
	for id, count := range result {
		if count == 0 {
			idsToQuery = append(idsToQuery, id)
		}
	}

	if len(idsToQuery) > 0 {
		if err := s.db.Model(&model.Article{}).Select("id, view_count").Where("id IN ?", idsToQuery).Find(&dbArticles).Error; err == nil {
			for _, article := range dbArticles {
				result[article.ID] = int64(article.ViewCount)
			}
		}
	}

	return result
}

func (s *ViewCountService) SyncToDatabase(ctx context.Context) error {
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
