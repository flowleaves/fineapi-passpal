// Command passpal 是 PassPal 凭据管理系统的唯一可执行入口。
//
// 子命令：
//
//	serve          启动 HTTP 服务
//	hash-password  生成管理员密码的 Argon2id hash
//	auth-unlock    解除某个来源 IP 的登录锁定（本地操作，无 HTTP 端点）
//	backup         手动触发一次在线备份
//	restore        离线恢复数据库（必须先停止服务）
//	version        打印版本
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	passpal "passpal"
	"passpal/internal/account"
	"passpal/internal/auth"
	"passpal/internal/backup"
	"passpal/internal/config"
	"passpal/internal/cryptoutil"
	"passpal/internal/database"
	"passpal/internal/export"
	"passpal/internal/httpapi"
	"passpal/internal/importer"
	"passpal/internal/project"
	"passpal/internal/search"
	"passpal/internal/tag"
	"passpal/internal/tasklock"
)

// Version 是构建版本号。
const Version = "1.0.0"

// maxSchemaVersion 是当前二进制支持的最高迁移版本（restore 校验用）。
//
// 从内嵌迁移文件推导，而不是写死 —— 手写常量在加了新迁移后极易忘记同步，
// 后果是恢复功能静默失效（恢复时报「版本高于当前支持」）。
var maxSchemaVersion = passpal.MaxMigrationVersion()

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		must(runServe(os.Args[2:]))
	case "hash-password":
		must(runHashPassword(os.Args[2:]))
	case "auth-unlock":
		must(runAuthUnlock(os.Args[2:]))
	case "backup":
		must(runBackup(os.Args[2:]))
	case "restore":
		must(runRestore(os.Args[2:]))
	case "version", "-v", "--version":
		fmt.Println("passpal", Version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令：%s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `PassPal —— 个人凭据管理系统

用法：
  passpal serve            启动 HTTP 服务
  passpal hash-password    生成管理员密码的 Argon2id hash（写入 .env 的 ADMIN_PASSWORD_HASH）
  passpal auth-unlock      解除来源 IP 的登录锁定：--source <IP>
  passpal backup           立即执行一次在线备份
  passpal restore          离线恢复：--from <备份绝对路径>
  passpal version          打印版本

配置全部来自进程环境（compose env_file / systemd EnvironmentFile），
本程序不读取 .env 文件。
`)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误："+err.Error())
		os.Exit(1)
	}
}

func newLogger(env string) *slog.Logger {
	level := slog.LevelInfo
	if env == config.EnvDevelopment {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// ── serve ───────────────────────────────────────────────────

func runServe(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	_ = flags.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.AppEnv)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	// slog 的 Info 不是 printf 风格，这里包一层，否则 %s 会原样打印。
	logf := func(format string, a ...any) { logger.Info(fmt.Sprintf(format, a...)) }

	applied, err := database.Migrate(ctx, db, migrationsFS(), "migrations", cfg.BackupDir, logf)
	if err != nil {
		return err
	}
	if applied > 0 {
		logger.Info("迁移完成", "applied", applied)
	}

	if err := database.SeedSettings(ctx, db, map[string]string{
		"session_ttl_hours":  itoa(int(cfg.SessionTTL.Hours())),
		"login_max_attempts": itoa(cfg.LoginMaxAttempt),
		"login_lock_minutes": itoa(int(cfg.LoginLockFor.Minutes())),
		"backup_retain_days": itoa(int(cfg.BackupRetain.Hours() / 24)),
	}, "startup"); err != nil {
		return err
	}

	cipher, err := cryptoutil.NewCipher(cfg.EncryptionKeys, cfg.CurrentKeyVersion)
	if err != nil {
		return fmt.Errorf("初始化加密器失败：%w", err)
	}

	authSvc, err := auth.New(ctx, db, cfg)
	if err != nil {
		return err
	}
	if err := authSvc.SyncAdminHashFingerprint(ctx); err != nil {
		return err
	}

	projects := project.New(db)
	tags := tag.New(db)
	accounts := account.New(db, cipher, projects, tags)
	importStore := importer.NewStore()
	deduper := importer.NewDeduper(db)
	committer := importer.NewCommitter(db, accounts, projects, tags)
	searchSvc := search.New(accounts)
	backupSvc, err := backup.New(db, cfg.BackupDir, cfg.BackupRetain, cfg.EncryptionKeys, cfg.CurrentKeyVersion)
	if err != nil {
		return err
	}
	exportSvc := export.New(db, accounts)

	staticFS, err := fs.Sub(passpal.WebDist, passpal.WebDistRoot)
	if err != nil {
		return fmt.Errorf("加载内嵌前端资源失败：%w", err)
	}

	server := httpapi.New(httpapi.Options{
		Config:    cfg,
		DB:        db,
		Cipher:    cipher,
		Auth:      authSvc,
		Projects:  projects,
		Tags:      tags,
		Accounts:  accounts,
		Imports:   importStore,
		Deduper:   deduper,
		Committer: committer,
		Search:    searchSvc,
		Backups:   backupSvc,
		Exports:   exportSvc,
		TaskLock:  tasklock.New(),
		Static:    staticFS,
	})

	addr := cfg.BindAddr + ":" + itoa(cfg.Port)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      5 * time.Minute, // 导出与备份下载可能较慢
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	go runBackgroundTasks(ctx, db, importStore, logger)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("PassPal 已启动",
			"addr", addr,
			"env", cfg.AppEnv,
			"origin", cfg.AppOrigin,
			"db", cfg.DatabasePath,
			"key_version", cfg.CurrentKeyVersion,
			"cookie_secure", cfg.CookieSecure(),
		)
		// 明文 HTTP 下凭据会以明文经网络传输；同时会话 cookie 不能带 Secure，
		// 否则浏览器会丢弃它（表现为登录成功但立刻「会话无效」）。
		// 这里只告警不阻止启动 —— 内网/隧道场景可能是刻意为之。
		if cfg.IsProduction() && cfg.UsesPlainHTTP() {
			logger.Warn("正在以明文 HTTP 提供服务：登录密码与账号凭据会不经加密地在网络中传输。" +
				"建议在前面加一层 TLS（反向代理或 SSH 隧道）。")
		}
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("收到退出信号，正在优雅关闭")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("优雅关闭失败", "err", err)
	}
	logger.Info("已停止")
	return nil
}

// runBackgroundTasks 定期清理会话、来源记录、草稿与过期审计。
func runBackgroundTasks(ctx context.Context, db *database.DB, store *importer.Store, logger *slog.Logger) {
	short := time.NewTicker(10 * time.Minute)
	long := time.NewTicker(time.Hour)
	defer short.Stop()
	defer long.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-short.C:
			_ = store.Cleanup()
		case <-long.C:
			if err := authCleanup(ctx, db); err != nil {
				logger.Warn("后台清理失败", "err", err)
			}
		}
	}
}

func authCleanup(ctx context.Context, db *database.DB) error {
	now := time.Now().Unix()
	if _, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM auth_state WHERE expires_at <= ?`, now); err != nil {
		return err
	}
	return nil
}

// ── hash-password ───────────────────────────────────────────

func runHashPassword(args []string) error {
	flags := flag.NewFlagSet("hash-password", flag.ExitOnError)
	password := flags.String("password", "", "直接提供密码（不推荐，会进入 shell 历史）")
	_ = flags.Parse(args)

	pw := *password
	if pw == "" {
		fmt.Fprint(os.Stderr, "请输入管理员密码：")
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("读取密码失败：%w", err)
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if pw == "" {
		return errors.New("密码不能为空")
	}

	hash, err := cryptoutil.HashPassword(pw, cryptoutil.DefaultArgon2Params)
	if err != nil {
		return err
	}
	fmt.Println(hash)
	fmt.Fprintln(os.Stderr, "\n把上面这一行原样写入 ADMIN_PASSWORD_HASH。")
	fmt.Fprintln(os.Stderr, "注意：hash 内含 $ 字符，.env 中用单引号包裹，不要让它被 shell 展开。")
	return nil
}

// ── auth-unlock ─────────────────────────────────────────────

func runAuthUnlock(args []string) error {
	flags := flag.NewFlagSet("auth-unlock", flag.ExitOnError)
	source := flags.String("source", "", "要解锁的来源 IP")
	_ = flags.Parse(args)
	if *source == "" {
		return errors.New("必须指定 --source <IP>")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	authSvc, err := auth.New(ctx, db, cfg)
	if err != nil {
		return err
	}
	found, err := authSvc.UnlockSource(ctx, *source)
	if err != nil {
		return err
	}
	if found {
		fmt.Printf("已解除来源 %s 的登录锁定。\n", *source)
	} else {
		fmt.Printf("来源 %s 当前没有锁定记录。\n", *source)
	}
	return nil
}

// ── backup ──────────────────────────────────────────────────

func runBackup(args []string) error {
	flags := flag.NewFlagSet("backup", flag.ExitOnError)
	_ = flags.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	svc, err := backup.New(db, cfg.BackupDir, cfg.BackupRetain, cfg.EncryptionKeys, cfg.CurrentKeyVersion)
	if err != nil {
		return err
	}
	info, err := svc.Run(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("备份完成：%s（%d 字节）\n", info.Name, info.Size)
	return nil
}

// ── restore ─────────────────────────────────────────────────

func runRestore(args []string) error {
	flags := flag.NewFlagSet("restore", flag.ExitOnError)
	from := flags.String("from", "", "备份文件的绝对路径")
	_ = flags.Parse(args)
	if *from == "" {
		return errors.New("必须指定 --from <备份绝对路径>")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()

	// 恢复必须针对本工具独立目录，且服务已停止。
	if err := assertServiceStopped(ctx, cfg.DatabasePath); err != nil {
		return err
	}

	return backup.Restore(ctx, backup.RestoreOptions{
		From:              *from,
		DatabasePath:      cfg.DatabasePath,
		Keys:              cfg.EncryptionKeys,
		CurrentKeyVersion: cfg.CurrentKeyVersion,
		AdminHash:         cfg.AdminPasswordHash,
		MaxSchemaVersion:  maxSchemaVersion,
		Logf:              func(format string, a ...any) { fmt.Printf(format+"\n", a...) },
	})
}

// assertServiceStopped 通过尝试取排他锁判断服务是否仍在运行。
func assertServiceStopped(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err != nil {
		return nil // 目标库不存在，视为未运行
	}
	db, err := database.Open(ctx, path)
	if err != nil {
		return fmt.Errorf("无法访问目标数据库：%w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		return errors.New("检测到数据库正在被其他进程使用，请先停止 PassPal 服务再执行恢复")
	}
	if _, err := db.ExecContext(ctx, "ROLLBACK"); err != nil {
		return err
	}
	return nil
}

// migrationsFS 返回内嵌的迁移目录。
func migrationsFS() fs.FS { return passpal.Migrations }

func itoa(n int) string { return fmt.Sprintf("%d", n) }
