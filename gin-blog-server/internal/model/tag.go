package model

import (
	"time"

	"gorm.io/gorm"
)

type Tag struct {
	Model
	Name     string `gorm:"unique;type:varchar(20);not null" json:"name"`
	IsDelete bool   `json:"is_delete"`

	Articles []*Article `gorm:"many2many:article_tag;" json:"articles,omitempty"`
}

type TagVO struct {
	ID        uint      `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Name         string `json:"name"`
	ArticleCount int    `json:"article_count"`
}

func GetTagList(db *gorm.DB, page, size int, keyword string) (list []TagVO, total int64, err error) {
	return GetTagListWithDeleted(db, page, size, keyword, nil)
}

func GetTagListWithDeleted(db *gorm.DB, page, size int, keyword string, isDelete *bool) (list []TagVO, total int64, err error) {
	db = db.Table("tag t").
		Joins("LEFT JOIN article_tag at ON t.id = at.tag_id").
		Select("t.id", "t.name", "COUNT(at.article_id) AS article_count", "t.created_at", "t.updated_at")

	if isDelete == nil {
		db = db.Where("t.is_delete = 0")
	} else {
		db = db.Where("t.is_delete = ?", isDelete)
	}

	if keyword != "" {
		db = db.Where("t.name LIKE ?", "%"+keyword+"%")
	}

	result := db.
		Group("t.id").Order("t.updated_at DESC").
		Count(&total).
		Scopes(Paginate(page, size)).
		Find(&list)

	return list, total, result.Error
}

func GetTagOption(db *gorm.DB) ([]OptionVO, error) {
	list := make([]OptionVO, 0)
	result := db.Model(&Tag{}).Where("is_delete = 0").Select("id", "name").Find(&list)
	return list, result.Error
}

// 根据 [文章id] 获取 [标签名称列表]
func GetTagNamesByArticleId(db *gorm.DB, id int) ([]string, error) {
	list := make([]string, 0)
	result := db.Table("tag").
		Joins("LEFT JOIN article_tag ON tag.id = article_tag.tag_id").
		Where("article_id", id).
		Where("tag.is_delete = 0").
		Pluck("name", &list)
	return list, result.Error
}

// 软删除标签
func UpdateTagSoftDelete(db *gorm.DB, ids []int, isDelete bool) (int64, error) {
	result := db.Model(Tag{}).
		Where("id IN ?", ids).
		Update("is_delete", isDelete)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func SaveOrUpdateTag(db *gorm.DB, id int, name string) (*Tag, error) {
	tag := Tag{
		Model: Model{ID: id},
		Name:  name,
	}

	var result *gorm.DB
	if id > 0 {
		result = db.Updates(tag)
	} else {
		result = db.Create(&tag)
	}

	return &tag, result.Error
}
