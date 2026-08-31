package sqlstore

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"gochen-runtime/internal/sqlsafe"
	"gochen/codec"
	"gochen/codec/idcodec"
	"gochen/db"
	"gochen/errors"
	"gochen/eventing/monitoring"
	"gochen/eventing/store"
	"gochen/observe/logging"
)

// SQLEventStore 基于通用 SQL 接口的事件存储。
type SQLEventStore[ID comparable] struct {
	db        db.IDatabase
	tableName string
	codec     codec.ICodec[ID, any]
	logger    logging.ILogger
	metrics   atomic.Value // eventStoreMetricsHolder（承载 monitoring.IEventStoreMetricsRecorder），用于并发热替换且避免 data race

	// globalPositionSupport 缓存 global_position 列确认支持结果（1=支持）。
	globalPositionSupport atomic.Int32
	globalPositionMu      sync.Mutex
	globalPositionProbe   globalPositionProbeCache
	streamHorizonMu       sync.Mutex
	streamHorizonCache    streamHorizonCache
}

type globalPositionProbeCache struct {
	expiresAt time.Time
	supported bool
}

type streamHorizonCache struct {
	expiresAt time.Time
	has       bool
	horizon   int64
}

var _ store.IEventStreamStore[int64] = (*SQLEventStore[int64])(nil)
var _ store.ITransactionalEventStore[int64] = (*SQLEventStore[int64])(nil)

var defaultNoopLogger logging.ILogger = logging.NewNoopLogger()

type sqLEventStoreOptions struct {
	logger logging.ILogger
}

// SQLEventStoreOption 用于配置 SQL 事件存储的可选项。
type SQLEventStoreOption func(*sqLEventStoreOptions)

type eventStoreMetricsHolder struct {
	rec monitoring.IEventStoreMetricsRecorder
}

// WithLogger 为 SQL 事件存储注入日志实现。
func WithLogger(logger logging.ILogger) SQLEventStoreOption {
	return func(o *sqLEventStoreOptions) { o.logger = logger }
}

// validateTableName 校验表名称。
func validateTableName(tableName string) error {
	return sqlsafe.ValidateTableName(tableName)
}

// NewSQLEventStoreWithCodec 创建SQL事件存储并带Codec。
func NewSQLEventStoreWithCodec[ID comparable](db db.IDatabase, tableName string, idCodec codec.ICodec[ID, any], opts ...SQLEventStoreOption) (*SQLEventStore[ID], error) {
	if db == nil {
		return nil, errors.NewCode(errors.InvalidInput, "NewSQLEventStoreWithCodec: db cannot be nil")
	}
	if tableName == "" {
		tableName = "event_store"
	}
	if err := validateTableName(tableName); err != nil {
		return nil, err
	}
	if idCodec == nil {
		return nil, errors.NewCode(errors.InvalidInput, "NewSQLEventStoreWithCodec: codec cannot be nil")
	}

	o := &sqLEventStoreOptions{
		logger: nil,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(o)
	}

	logger := o.logger
	if logger == nil {
		// Avoid nil panics. Composition root should inject a real logger.
		logger = defaultNoopLogger
	}
	return &SQLEventStore[ID]{db: db, tableName: tableName, codec: idCodec, logger: logger}, nil
}

// NewSQLEventStore 为 `int64` 聚合 ID 创建一个 SQL 事件存储。
func NewSQLEventStore(db db.IDatabase, tableName string, opts ...SQLEventStoreOption) (*SQLEventStore[int64], error) {
	return NewSQLEventStoreWithCodec[int64](db, tableName, idcodec.NewInt64[int64](), opts...)
}

// Init 通过一次 Ping 校验底层数据库是否可用。
func (s *SQLEventStore[ID]) Init(ctx context.Context) error { return s.db.Ping(ctx) }

// GetDB 返回底层数据库适配器，便于需要事务复用的场景接入。
func (s *SQLEventStore[ID]) GetDB() db.IDatabase { return s.db }

func (s *SQLEventStore[ID]) GetTableName() string { return s.tableName }

// GetCodec 返回当前使用的聚合 ID 编解码器。
func (s *SQLEventStore[ID]) GetCodec() codec.ICodec[ID, any] { return s.codec }

// SetMetricsRecorder 设置事件存储指标记录器（可选）。
//
// 并发语义：允许在运行时注入/替换 recorder（内部使用 atomic.Value），避免与 Record* 并发访问时产生 data race。
func (s *SQLEventStore[ID]) SetMetricsRecorder(rec monitoring.IEventStoreMetricsRecorder) {
	// atomic.Value 不允许 Store(nil)，且要求后续 Store 的动态类型一致。
	// 用稳定的 holder 类型承载 interface，可以安全地设置 nil/不同实现。
	s.metrics.Store(eventStoreMetricsHolder{rec: rec})
}

func (s *SQLEventStore[ID]) getMetrics() monitoring.IEventStoreMetricsRecorder {
	if s == nil {
		return nil
	}
	v := s.metrics.Load()
	if v == nil {
		return nil
	}
	h, ok := v.(eventStoreMetricsHolder)
	if !ok {
		return nil
	}
	return h.rec
}

// recordEventStoreError 在注入 recorder 时上报一次事件存储错误。
func (s *SQLEventStore[ID]) recordEventStoreError() {
	if m := s.getMetrics(); m != nil {
		m.RecordEventStoreError()
	}
}

// recordEventSaved 上报一批事件保存的数量和耗时。
func (s *SQLEventStore[ID]) recordEventSaved(count int, d time.Duration) {
	if m := s.getMetrics(); m != nil {
		m.RecordEventSaved(count, d)
	}
}

// recordEventLoaded 上报一批事件加载的数量和耗时。
func (s *SQLEventStore[ID]) recordEventLoaded(count int, d time.Duration) {
	if m := s.getMetrics(); m != nil {
		m.RecordEventLoaded(count, d)
	}
}

// getLogger 返回一个可安全使用的 logger；缺失时回退到 noop。
func (s *SQLEventStore[ID]) getLogger() logging.ILogger {
	if s == nil || s.logger == nil {
		return defaultNoopLogger
	}
	return s.logger
}
