package migrate

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"

	"gochen/contextx"
	"gochen/db"
	"gochen/errors"
)

type lockHeartbeatFailure struct {
	err error
}

func (r *Runner) acquireLock(ctx context.Context) error {
	if r.lockTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.lockTimeout)
		defer cancel()
	}
	token := r.nextLockToken()
	deadline := time.Now().Add(r.lockTimeout)
	for {
		acquired, err := r.tryAcquireLock(ctx, token)
		if err == nil && acquired {
			r.lockToken = token
			r.lockErr.Store(nil)
			r.startLockHeartbeat()
			return nil
		}
		if err != nil && !r.isLockConflict(err) {
			return err
		}
		if waitErr := waitForLockRetry(ctx, deadline, r.lockTimeout); waitErr != nil {
			return waitErr
		}
	}
}

func (r *Runner) tryAcquireLock(ctx context.Context, token uint64) (bool, error) {
	now := r.now().UTC()
	insert := fmt.Sprintf(
		"INSERT INTO %s (migration_type, version, dirty, updated_at) VALUES (?, ?, ?, ?)",
		r.quotedStateTable(),
	)
	if _, err := r.db.Exec(ctx, insert, lockMigrationType, token, 0, now); err == nil {
		return true, nil
	} else if !r.isLockConflict(err) {
		return false, errors.Wrap(err, errors.Database, "acquire migration lock failed").
			WithContext("table", r.stateTable)
	}
	if r.lockStaleAfter <= 0 {
		return false, ErrLocked
	}

	update := fmt.Sprintf(
		"UPDATE %s SET version = ?, dirty = ?, updated_at = ? WHERE migration_type = ? AND updated_at <= ?",
		r.quotedStateTable(),
	)
	result, err := r.db.Exec(ctx, update, token, 0, now, lockMigrationType, now.Add(-r.lockStaleAfter))
	if err != nil {
		return false, errors.Wrap(err, errors.Database, "reclaim stale migration lock failed").
			WithContext("table", r.stateTable)
	}
	affected, err := rowsAffected(result)
	if err != nil {
		return false, errors.Wrap(err, errors.Database, "read migration lock reclaim result failed").
			WithContext("table", r.stateTable)
	}
	if affected == 0 {
		return false, ErrLocked
	}
	return true, nil
}

func (r *Runner) releaseLock() {
	if r.lockToken == 0 {
		return
	}
	r.stopLockHeartbeat()
	token := r.lockToken
	r.lockToken = 0
	query := fmt.Sprintf("DELETE FROM %s WHERE migration_type = ? AND version = ?", r.quotedStateTable())
	ctx, cancel := context.WithTimeout(contextx.Background(), 5*time.Second)
	defer cancel()
	// 删除锁记录失败不影响主流程：锁最终会被 stale 回收机制清理。
	if _, err := r.db.Exec(ctx, query, lockMigrationType, token); err != nil {
		// 无 logger 依赖，静默忽略；stale lock 回收机制将兜底清理残留锁记录。
		_ = err
	}
}

func (r *Runner) startLockHeartbeat() {
	interval := lockHeartbeatInterval(r.lockStaleAfter)
	if interval <= 0 || r.lockToken == 0 {
		return
	}
	r.lockErr.Store(nil)
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	r.lockStop = stopCh
	r.lockDone = doneCh
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(doneCh)
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				if err := r.refreshLockWithDB(contextx.Background(), r.db); err != nil {
					r.lockErr.Store(&lockHeartbeatFailure{err: err})
					return
				}
			}
		}
	}()
}

func (r *Runner) stopLockHeartbeat() {
	stopCh := r.lockStop
	doneCh := r.lockDone
	r.lockStop = nil
	r.lockDone = nil
	if stopCh == nil {
		return
	}
	close(stopCh)
	if doneCh == nil {
		return
	}
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
	}
}

func (r *Runner) refreshLock(ctx context.Context) error {
	if err := r.lockHeartbeatError(); err != nil {
		return err
	}
	return r.refreshLockWithDB(ctx, r.db)
}

func (r *Runner) refreshLockWithDB(ctx context.Context, database db.IDatabase) error {
	if r.lockToken == 0 {
		return nil
	}
	if err := r.lockHeartbeatError(); err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET updated_at = ? WHERE migration_type = ? AND version = ?", r.quotedStateTable())
	result, err := database.Exec(ctx, query, r.now().UTC(), lockMigrationType, r.lockToken)
	if err != nil {
		return errors.Wrap(err, errors.Database, "refresh migration lock failed").
			WithContext("table", r.stateTable)
	}
	affected, err := rowsAffected(result)
	if err != nil {
		return errors.Wrap(err, errors.Database, "read migration lock refresh result failed").
			WithContext("table", r.stateTable)
	}
	if affected == 0 {
		return ErrLocked
	}
	return nil
}

func (r *Runner) lockHeartbeatError() error {
	if r == nil {
		return nil
	}
	failure := r.lockErr.Load()
	if failure == nil || failure.err == nil {
		return nil
	}
	return failure.err
}

func (r *Runner) nextLockToken() uint64 {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return lockTokenFromBytes(buf)
}

func lockTokenFromBytes(buf [8]byte) uint64 {
	token := binary.BigEndian.Uint64(buf[:]) >> 1
	if token == 0 {
		return 1
	}
	return token
}

func (r *Runner) isLockConflict(err error) bool {
	return err != nil && (errors.Is(err, ErrLocked) || r.dialect.IsUniqueViolation(err))
}

func lockHeartbeatInterval(staleAfter time.Duration) time.Duration {
	if staleAfter <= 0 {
		return 0
	}
	interval := staleAfter / 3
	if interval <= 0 {
		return 0
	}
	if interval < 10*time.Millisecond {
		return 10 * time.Millisecond
	}
	return interval
}
