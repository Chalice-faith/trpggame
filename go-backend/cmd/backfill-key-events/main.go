// backfill-key-events is an offline administrator tool, never a public endpoint.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"trpggame/internal/config"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	room := flag.Uint("room", 0, "要补录的房间 ID（必填）")
	timeline := flag.String("timeline", "", "要补录的时间线 UUID（必填，不扫描祖先）")
	after := flag.Uint64("after", 0, "上次成功批次的 next_position")
	throughFlag := flag.String("through", "", "上次批次的 through_position；首次省略以冻结当前水位")
	batch := flag.Int("batch", 100, "每个事务最多扫描的记录数（1—100）")
	maxBatches := flag.Int("max-batches", 1, "本次最多处理的批次数（1—1000）")
	apply := flag.Bool("apply", false, "写入缺失事件；默认仅预览")
	flag.Parse()
	if flag.NArg() != 0 || *room == 0 || !model.ValidMemoryUUID(*timeline) || *batch < 1 || *batch > 100 || *maxBatches < 1 || *maxBatches > 1000 {
		return fmt.Errorf("参数无效：必须指定有效 --room / --timeline，batch=1—100，max-batches=1—1000")
	}
	var through *uint64
	if *throughFlag != "" {
		position, err := strconv.ParseUint(*throughFlag, 10, 64)
		if err != nil {
			return fmt.Errorf("--through 必须是非负整数")
		}
		through = &position
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("加载配置失败：%w", err)
	}
	// Silent SQL logging keeps JSON stdout usable and never prints source text.
	db, err := gorm.Open(mysql.Open(cfg.Database.DSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return fmt.Errorf("连接 MySQL 失败，请检查 database 配置")
	}
	connection, err := db.DB()
	if err != nil {
		return fmt.Errorf("获取 MySQL 连接失败")
	}
	defer connection.Close()
	connection.SetMaxOpenConns(2)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store := repo.NewGameMemoryRepo(db)
	encoder := json.NewEncoder(os.Stdout)
	for i := 0; i < *maxBatches; i++ {
		batchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result, err := store.BackfillKeyEvents(batchCtx, repo.KeyEventBackfillRequest{
			RoomID: *room, TimelineID: *timeline, AfterPosition: *after, ThroughPosition: through, Limit: *batch, Apply: *apply,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("补录批次失败（失败批次未推进检查点）：%w", err)
		}
		if err := encoder.Encode(result); err != nil {
			return fmt.Errorf("输出检查点失败；使用上一个检查点重试：%w", err)
		}
		*after, through = result.NextPosition, &result.ThroughPosition
		if result.Done {
			break
		}
	}
	return nil
}
