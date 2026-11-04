package gormx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSQLiteDriverConflictRepro 是 #72 的炸点复现对（正/负对照）：
//
//   - 正对照：只引 gormx 根包 + modernc.org/sqlite ⇒ 必须干净启动。
//     修复前该程序 init 即 panic（glebarez 随根包被链接、与 modernc 双注册
//     "sqlite"）——这条是真回归门禁；
//   - 负对照：gormx/sqlitedrv + modernc.org/sqlite ⇒ 必须复现 panic。
//     两者驱动注册副作用同名是上游行为（包文档已声明边界），此对照固化边界，
//     也防止「测试永远绿」的空转（哪天 panic 消失了会有人来看）。
//
// 走 go run 子进程：panic 发生在 init，无法在同一测试二进制内观测
// （测试二进制自己就链接 glebarez）。
func TestSQLiteDriverConflictRepro(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("环境无 go 工具链，跳过炸点复现")
	}
	root := moduleRoot(t)

	cases := []struct {
		name      string
		dir       string
		wantPanic bool
	}{
		{"根包+modernc 共存（修复后应干净启动）", "nopanic", false},
		{"sqlitedrv+modernc 仍冲突（边界声明）", "panic", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, goBin, "run", "./testdata/sqlite_conflict_repro/"+tc.dir)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GOWORK=off")
			out, err := cmd.CombinedOutput()
			if tc.wantPanic {
				if err == nil {
					t.Fatalf("预期 init panic（双注册 \"sqlite\"），实际干净退出；边界变了？输出:\n%s", out)
				}
				if !strings.Contains(string(out), "Register called twice for driver") {
					t.Fatalf("失败原因不是驱动双注册，输出:\n%s", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("根包+modernc 应可共存（#72 修复目标），go run 失败: %v\n输出:\n%s", err, out)
			}
			if !strings.Contains(string(out), "OK") {
				t.Fatalf("复现程序未跑到 main，输出:\n%s", out)
			}
		})
	}
}

// 防误删：testdata 复现程序是本测试的被测物。
func TestConflictReproProgramsExist(t *testing.T) {
	root := moduleRoot(t)
	for _, p := range []string{
		"testdata/sqlite_conflict_repro/nopanic/main.go",
		"testdata/sqlite_conflict_repro/panic/main.go",
	} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("复现程序缺失 %s: %v", p, err)
		}
	}
}
