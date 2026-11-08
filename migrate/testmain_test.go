package migrate

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openMigrateTestDB 打开 sqlite 内存库（migrate 子包的测试专用 helper；
// 生产代码零 sqlite 驱动链接的门禁只断言非 _test 文件）。
//
// #117：DSN 必须带轮次唯一后缀（uuid，同根包 newTestDB 先例）——此前只按
// t.Name() 命名 + cache=shared：共享缓存内存库在进程存活期内同 DSN 指向
// 同一个库，而 t.Name() 在 -count=N 下每轮完全相同 ⇒ 第二轮拿到第一轮
// 已建表/插数的残留库，INSERT 撞 UNIQUE constraint failed（3 个用例恒红）。
func openMigrateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.New().String() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}
