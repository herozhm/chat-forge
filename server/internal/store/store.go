package store

import (
	"os"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"chat-forge-server/internal/model"
)

// Open 打开(必要时创建)SQLite 存储并迁移表结构。
// glebarez/sqlite 是纯 Go 实现,无 cgo,交叉编译与 CI 零负担。
func Open(path string) (*gorm.DB, error) {
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return nil, err
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&model.User{}, &model.Conversation{}, &model.Message{}); err != nil {
		return nil, err
	}
	return db, nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
