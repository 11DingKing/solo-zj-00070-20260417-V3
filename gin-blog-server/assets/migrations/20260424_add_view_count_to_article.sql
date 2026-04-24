-- 为 article 表添加 view_count 字段
-- 执行此脚本以添加阅读量统计字段

ALTER TABLE `article` ADD COLUMN `view_count` int NOT NULL DEFAULT 0 COMMENT '阅读量' AFTER `original_url`;
