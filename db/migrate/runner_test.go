package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/db/dialect"
	goerrors "gochen/errors"

	_ "modernc.org/sqlite"
)

func TestRunnerUpDownForceAndDirty(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL);`)
	writeMigrationFile(t, tempDir, "000001_create_users.down.sql", `DROP TABLE users;`)
	writeMigrationFile(t, tempDir, "000002_create_profiles.up.sql", `CREATE TABLE profiles (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL);`)
	writeMigrationFile(t, tempDir, "000002_create_profiles.down.sql", `DROP TABLE profiles;`)
	writeMigrationFile(t, tempDir, "000003_broken.up.sql", `CREATE TABL broken (`) // 故意出错，验证 dirty 状态

	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}

	database := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	err = runner.Up(context.Background())
	if err == nil {
		t.Fatal("expected broken migration to fail")
	}
	status, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !status.Dirty || status.Version != 3 {
		t.Fatalf("unexpected dirty status: %#v", status)
	}

	if err := runner.Force(context.Background(), 2); err != nil {
		t.Fatalf("Force failed: %v", err)
	}

	status, err = runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status after force failed: %v", err)
	}
	if status.Dirty || status.Version != 2 {
		t.Fatalf("unexpected status after force: %#v", status)
	}

	if err := runner.Down(context.Background()); err != nil {
		t.Fatalf("Down failed: %v", err)
	}
	status, err = runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status after down failed: %v", err)
	}
	if status.Dirty || status.Version != 1 {
		t.Fatalf("unexpected status after down: %#v", status)
	}

	assertTableExists(t, database, "users", true)
	assertTableExists(t, database, "profiles", false)
}

func TestDirtyStateErrorPreservesSentinelAndCode(t *testing.T) {
	err := dirtyStateError(7)
	if !errors.Is(err, ErrDirty) {
		t.Fatalf("dirtyStateError() = %v, want ErrDirty", err)
	}
	if code := goerrors.Code(err); code != goerrors.FailedPrecondition {
		t.Fatalf("dirtyStateError() code = %s", code)
	}
}

func TestRunnerUpNoChangeAndNilVersion(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL);`)
	writeMigrationFile(t, tempDir, "000001_create_users.down.sql", `DROP TABLE users;`)

	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	database := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	if err := runner.Up(context.Background()); err != nil {
		t.Fatalf("first Up failed: %v", err)
	}
	if err := runner.Up(context.Background()); !errors.Is(err, ErrNoChange) {
		t.Fatalf("second Up error = %v, want ErrNoChange", err)
	}

	if err := runner.Down(context.Background()); err != nil {
		t.Fatalf("Down failed: %v", err)
	}
	if err := runner.Down(context.Background()); !errors.Is(err, ErrNilVersion) {
		t.Fatalf("second Down error = %v, want ErrNilVersion", err)
	}
}

func TestRunnerMigrationTypeKeepsIndependentVersions(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001.schema.create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL);`)
	writeMigrationFile(t, tempDir, "000001.seed.seed_users.up.sql", `INSERT INTO users (id, name) VALUES (1, 'seed');`)

	database := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	schemaRunner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner schema failed: %v", err)
	}
	seedRunner, err := NewRunner(database, source, WithMigrationType("seed"))
	if err != nil {
		t.Fatalf("NewRunner seed failed: %v", err)
	}

	if err := schemaRunner.Up(context.Background()); err != nil {
		t.Fatalf("schema Up failed: %v", err)
	}
	if _, _, err := seedRunner.Version(context.Background()); !errors.Is(err, ErrNilVersion) {
		t.Fatalf("seed Version error = %v, want ErrNilVersion", err)
	}
	if err := seedRunner.Up(context.Background()); err != nil {
		t.Fatalf("seed Up failed: %v", err)
	}

	schemaStatus, err := schemaRunner.Status(context.Background())
	if err != nil {
		t.Fatalf("schema Status failed: %v", err)
	}
	seedStatus, err := seedRunner.Status(context.Background())
	if err != nil {
		t.Fatalf("seed Status failed: %v", err)
	}
	if schemaStatus.Type != defaultMigrationType || schemaStatus.Version != 1 || schemaStatus.Dirty {
		t.Fatalf("unexpected schema status: %#v", schemaStatus)
	}
	if seedStatus.Type != "seed" || seedStatus.Version != 1 || seedStatus.Dirty {
		t.Fatalf("unexpected seed status: %#v", seedStatus)
	}
}

func TestRunnerRejectsNonGochenStateTable(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)

	database := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	_, err := database.Exec(context.Background(), `
CREATE TABLE schema_migrations (
  version BIGINT NOT NULL DEFAULT 0,
  dirty INTEGER NOT NULL DEFAULT 0
)`)
	if err != nil {
		t.Fatalf("create non-gochen state table failed: %v", err)
	}
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	if err := runner.Up(context.Background()); err == nil || !strings.Contains(err.Error(), "not gochen format") {
		t.Fatalf("Up error = %v, want non-gochen state table rejection", err)
	}
}

func TestRunnerStepsMigrateVersionAndMultiStatement(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `
CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
INSERT INTO users (id, name) VALUES (1, 'a; b');
`)
	writeMigrationFile(t, tempDir, "000001_create_users.down.sql", `DROP TABLE users;`)
	writeMigrationFile(t, tempDir, "000002_create_profiles.up.sql", `CREATE TABLE profiles (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL);`)
	writeMigrationFile(t, tempDir, "000002_create_profiles.down.sql", `DROP TABLE profiles;`)
	writeMigrationFile(t, tempDir, "000003_create_tasks.up.sql", `CREATE TABLE tasks (id INTEGER PRIMARY KEY);`)
	writeMigrationFile(t, tempDir, "000003_create_tasks.down.sql", `DROP TABLE tasks;`)

	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	database := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	if err := runner.Steps(context.Background(), 2); err != nil {
		t.Fatalf("Steps up failed: %v", err)
	}
	version, dirty, err := runner.Version(context.Background())
	if err != nil {
		t.Fatalf("Version failed: %v", err)
	}
	if version != 2 || dirty {
		t.Fatalf("Version = (%d, %v), want (2, false)", version, dirty)
	}
	assertTableExists(t, database, "users", true)
	assertTableExists(t, database, "profiles", true)
	assertTableExists(t, database, "tasks", false)

	if err := runner.Migrate(context.Background(), 3); err != nil {
		t.Fatalf("Migrate up failed: %v", err)
	}
	assertTableExists(t, database, "tasks", true)

	if err := runner.Migrate(context.Background(), 1); err != nil {
		t.Fatalf("Migrate down failed: %v", err)
	}
	assertTableExists(t, database, "users", true)
	assertTableExists(t, database, "profiles", false)
	assertTableExists(t, database, "tasks", false)
}

func TestRunnerDownWritesFinalCleanVersionOnce(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)
	writeMigrationFile(t, tempDir, "000001_create_users.down.sql", `DROP TABLE users;`)
	writeMigrationFile(t, tempDir, "000002_create_profiles.up.sql", `CREATE TABLE profiles (id INTEGER PRIMARY KEY);`)
	writeMigrationFile(t, tempDir, "000002_create_profiles.down.sql", `DROP TABLE profiles;`)

	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	database := &recordingDB{IDatabase: openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))}
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	if err := runner.Up(context.Background()); err != nil {
		t.Fatalf("Up failed: %v", err)
	}

	database.statusWrites = nil
	if err := runner.Down(context.Background()); err != nil {
		t.Fatalf("Down failed: %v", err)
	}
	want := []Status{
		{Type: defaultMigrationType, Version: 2, Dirty: true},
		{Type: defaultMigrationType, Version: 1, Dirty: false},
	}
	if len(database.statusWrites) != len(want) {
		t.Fatalf("status writes = %#v, want %#v", database.statusWrites, want)
	}
	for i := range want {
		if database.statusWrites[i] != want[i] {
			t.Fatalf("status writes = %#v, want %#v", database.statusWrites, want)
		}
	}
	status, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status.Version != 1 || status.Dirty {
		t.Fatalf("unexpected status after down: %#v", status)
	}
}

func TestRunnerWriteCompatStatusRetriesUpdateAfterConcurrentInsert(t *testing.T) {
	runner := &Runner{
		dialect:       dialect.New("unknown"),
		stateTable:    defaultStateTable,
		migrationType: defaultMigrationType,
	}
	database := &compatStatusRaceDB{}

	err := runner.writeCompatStatusWithDB(context.Background(), database, defaultMigrationType, Status{
		Type:    defaultMigrationType,
		Version: 7,
		Dirty:   true,
	}, time.Unix(1, 0))
	if err != nil {
		t.Fatalf("writeCompatStatusWithDB failed: %v", err)
	}
	if database.inserts != 1 {
		t.Fatalf("expected one insert attempt, got %d", database.inserts)
	}
	if database.updates != 1 {
		t.Fatalf("expected fallback update after unique violation, got %d", database.updates)
	}
}

func TestRunnerWriteStatusUsesUpsertOnModernStateTable(t *testing.T) {
	t.Parallel()

	database := &queryCaptureDB{IDatabase: openSQLiteDB(t, filepath.Join(t.TempDir(), "runner-upsert.db"))}
	runner := &Runner{
		db:            database,
		dialect:       dialect.New("sqlite"),
		migrationType: defaultMigrationType,
		stateTable:    defaultStateTable,
		now:           time.Now,
	}
	if err := runner.ensureStateTable(context.Background()); err != nil {
		t.Fatalf("ensureStateTable failed: %v", err)
	}
	if err := runner.writeStatusWithDB(context.Background(), database, Status{Version: 1, Dirty: true}); err != nil {
		t.Fatalf("writeStatusWithDB dirty failed: %v", err)
	}
	if err := runner.writeStatusWithDB(context.Background(), database, Status{Version: 2, Dirty: false}); err != nil {
		t.Fatalf("writeStatusWithDB clean failed: %v", err)
	}
	status, err := runner.readStatus(context.Background())
	if err != nil {
		t.Fatalf("readStatus failed: %v", err)
	}
	if status.Version != 2 || status.Dirty {
		t.Fatalf("unexpected status: %#v", status)
	}
	if database.hasQuery("SELECT COUNT(1)") {
		t.Fatalf("expected modern state writes to avoid SELECT COUNT existence checks, got %#v", database.queries)
	}
	if !database.hasExec("ON CONFLICT") {
		t.Fatalf("expected UPSERT write, got %#v", database.execs)
	}
}

func TestRunnerRefreshesLockOutsideMigrationTransaction(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	database := &lockRefreshCaptureDB{IDatabase: openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))}
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	if err := runner.Up(context.Background()); err != nil {
		t.Fatalf("Up failed: %v", err)
	}
	if database.txRefreshes != 0 {
		t.Fatalf("expected no lock refresh inside migration transaction, got %d", database.txRefreshes)
	}
}

func TestRunnerRefreshesLockForNonTransactionalMigration(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	database := &lockRefreshCaptureDB{IDatabase: openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))}
	runner, err := NewRunner(database, source, WithoutTransaction())
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	if err := runner.Up(context.Background()); err != nil {
		t.Fatalf("Up failed: %v", err)
	}
	if database.rootRefreshes == 0 {
		t.Fatal("expected lock refreshes through root database")
	}
	if database.txRefreshes != 0 {
		t.Fatalf("expected no transaction lock refreshes, got %d", database.txRefreshes)
	}
}

func TestRunnerFSSource(t *testing.T) {
	t.Parallel()

	source, err := NewFS(fstest.MapFS{
		"migrations/000001_create_users.up.sql":   {Data: []byte(`CREATE TABLE users (id INTEGER PRIMARY KEY);`)},
		"migrations/000001_create_users.down.sql": {Data: []byte(`DROP TABLE users;`)},
	}, "migrations")
	if err != nil {
		t.Fatalf("NewFS failed: %v", err)
	}

	tempDir := t.TempDir()
	database := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	if err := runner.Up(context.Background()); err != nil {
		t.Fatalf("Up failed: %v", err)
	}
	assertTableExists(t, database, "users", true)
}

func TestRunnerWithLockUsesDatabaseStateRow(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	dbPath := filepath.Join(tempDir, "runner.db")
	firstDB := openSQLiteDB(t, dbPath)
	secondDB := openSQLiteDB(t, dbPath)
	first, err := NewRunner(firstDB, source)
	if err != nil {
		t.Fatalf("NewRunner first failed: %v", err)
	}
	second, err := NewRunner(secondDB, source, WithLockTimeout(0))
	if err != nil {
		t.Fatalf("NewRunner second failed: %v", err)
	}

	err = first.WithLock(context.Background(), func() error {
		if err := second.WithLock(context.Background(), func() error { return nil }); !errors.Is(err, ErrLocked) {
			t.Fatalf("second WithLock error = %v, want ErrLocked", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("first WithLock failed: %v", err)
	}
}

func TestRunnerWithLockRejectsNilCallback(t *testing.T) {
	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	database := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	err = runner.WithLock(context.Background(), nil)
	if err == nil || !goerrors.Is(err, goerrors.InvalidInput) {
		t.Fatalf("WithLock nil callback error = %v, want invalid input", err)
	}
}

func TestLockTokenFromBytesFitsSignedBigInt(t *testing.T) {
	token := lockTokenFromBytes([8]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	if token == 0 {
		t.Fatal("token should not be zero")
	}
	if token > uint64(1<<63-1) {
		t.Fatalf("token = %d, want signed BIGINT range", token)
	}
}

func TestRunnerWithLockReturnsHeartbeatRefreshError(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	database := &heartbeatFailDB{
		IDatabase: openSQLiteDB(t, filepath.Join(tempDir, "runner.db")),
		failed:    make(chan struct{}),
	}
	runner, err := NewRunner(database, source, WithLockStaleAfter(30*time.Millisecond))
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	err = runner.WithLock(context.Background(), func() error {
		select {
		case <-database.failed:
			return nil
		case <-time.After(time.Second):
			return fmt.Errorf("heartbeat refresh was not attempted")
		}
	})
	if err == nil || !strings.Contains(err.Error(), "heartbeat refresh failed") {
		t.Fatalf("WithLock error = %v, want heartbeat refresh failure", err)
	}
}

func TestRunnerReclaimsStaleStateLock(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	database := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	now := time.Now().UTC()
	runner, err := NewRunner(database, source, WithLockTimeout(0), WithLockStaleAfter(10*time.Millisecond))
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	runner.now = func() time.Time { return now }
	if err := runner.ensureStateTable(context.Background()); err != nil {
		t.Fatalf("ensureStateTable failed: %v", err)
	}
	_, err = database.Exec(
		context.Background(),
		"INSERT INTO schema_migrations (migration_type, version, dirty, updated_at) VALUES (?, ?, ?, ?)",
		lockMigrationType,
		uint64(1),
		0,
		now.Add(-time.Minute),
	)
	if err != nil {
		t.Fatalf("insert stale lock failed: %v", err)
	}

	if err := runner.WithLock(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("WithLock failed: %v", err)
	}
	row := database.QueryRow(context.Background(), "SELECT COUNT(1) FROM schema_migrations WHERE migration_type = ?", lockMigrationType)
	var count int
	if err := row.Scan(&count); err != nil {
		t.Fatalf("read lock count failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("lock rows = %d, want 0", count)
	}
}

func TestRunnerReclaimLockReturnsRowsAffectedError(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	base := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	rowsErr := errors.New("rows affected failed")
	database := &rowsAffectedErrorDB{
		IDatabase: base,
		err:       rowsErr,
		match:     isStaleLockReclaimQuery,
	}
	now := time.Now().UTC()
	runner, err := NewRunner(database, source, WithLockTimeout(0), WithLockStaleAfter(10*time.Millisecond))
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	runner.now = func() time.Time { return now }
	if err := runner.ensureStateTable(context.Background()); err != nil {
		t.Fatalf("ensureStateTable failed: %v", err)
	}
	_, err = base.Exec(
		context.Background(),
		"INSERT INTO schema_migrations (migration_type, version, dirty, updated_at) VALUES (?, ?, ?, ?)",
		lockMigrationType,
		uint64(1),
		0,
		now.Add(-time.Minute),
	)
	if err != nil {
		t.Fatalf("insert stale lock failed: %v", err)
	}

	err = runner.WithLock(context.Background(), func() error { return nil })
	if !errors.Is(err, rowsErr) {
		t.Fatalf("WithLock error = %v, want rows affected error", err)
	}
	if errors.Is(err, ErrLocked) {
		t.Fatalf("WithLock error = %v, should not be ErrLocked", err)
	}
}

func TestRunnerRefreshLockReturnsRowsAffectedError(t *testing.T) {
	t.Parallel()

	source, err := NewFS(fstest.MapFS{
		"000001_create_users.up.sql": {Data: []byte(`CREATE TABLE users (id INTEGER PRIMARY KEY);`)},
	}, ".")
	if err != nil {
		t.Fatalf("NewFS failed: %v", err)
	}
	base := openSQLiteDB(t, filepath.Join(t.TempDir(), "runner.db"))
	rowsErr := errors.New("rows affected failed")
	database := &rowsAffectedErrorDB{
		IDatabase: base,
		err:       rowsErr,
		match:     isLockRefreshQuery,
	}
	runner, err := NewRunner(database, source, WithLockTimeout(0))
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	if err := runner.ensureStateTable(context.Background()); err != nil {
		t.Fatalf("ensureStateTable failed: %v", err)
	}
	runner.lockToken = 7
	_, err = base.Exec(
		context.Background(),
		"INSERT INTO schema_migrations (migration_type, version, dirty, updated_at) VALUES (?, ?, ?, ?)",
		lockMigrationType,
		runner.lockToken,
		0,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("insert lock failed: %v", err)
	}

	err = runner.refreshLockWithDB(context.Background(), database)
	if !errors.Is(err, rowsErr) {
		t.Fatalf("refreshLockWithDB error = %v, want rows affected error", err)
	}
	if errors.Is(err, ErrLocked) {
		t.Fatalf("refreshLockWithDB error = %v, should not be ErrLocked", err)
	}
}

func TestRunnerStatusDoesNotCreateStateTable(t *testing.T) {
	t.Parallel()

	source, err := NewFS(fstest.MapFS{
		"000001_create_users.up.sql": {Data: []byte(`CREATE TABLE users (id INTEGER PRIMARY KEY);`)},
	}, ".")
	if err != nil {
		t.Fatalf("NewFS failed: %v", err)
	}
	database := &queryCaptureDB{IDatabase: openSQLiteDB(t, filepath.Join(t.TempDir(), "runner.db"))}
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	status, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status.Type != defaultMigrationType || status.Version != 0 || status.Dirty {
		t.Fatalf("unexpected status: %#v", status)
	}
	if database.hasExec("CREATE TABLE IF NOT EXISTS") {
		t.Fatalf("Status should not create migration state table, execs=%#v", database.execs)
	}
	assertTableExists(t, database, defaultStateTable, false)
}

func TestRunnerRejectsUnsafeStateTable(t *testing.T) {
	t.Parallel()

	source, err := NewFS(fstest.MapFS{
		"000001_create_users.up.sql": {Data: []byte(`CREATE TABLE users (id INTEGER PRIMARY KEY);`)},
	}, ".")
	if err != nil {
		t.Fatalf("NewFS failed: %v", err)
	}
	database := openSQLiteDB(t, filepath.Join(t.TempDir(), "runner.db"))

	if _, err := NewRunner(database, source, WithStateTable(`schema_migrations;DROP TABLE users`)); err == nil {
		t.Fatal("expected unsafe state table to be rejected")
	}
	if _, err := NewRunner(database, source, WithMigrationType("seed-data")); err == nil {
		t.Fatal("expected unsafe migration type to be rejected")
	}
	if _, err := NewRunner(database, source, WithMigrationType(lockMigrationType)); err == nil {
		t.Fatal("expected reserved migration type to be rejected")
	}
}

func TestRunnerRejectsReservedMigrationTypeInSource(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001.__gochen_migration_lock.bad.up.sql", `SELECT 1;`)
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	_, err = source.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("List error = %v, want reserved migration type", err)
	}
}

func TestRunnerRejectsZeroVersionInSource(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000000_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	_, err = source.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "greater than zero") {
		t.Fatalf("List error = %v, want zero version rejection", err)
	}
}

func TestRunnerMethodsFailFastInsideWithLockCallback(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	database := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	err = runner.WithLock(context.Background(), func() error {
		_, innerErr := runner.Status(context.Background())
		return innerErr
	})
	if err == nil || !strings.Contains(err.Error(), "WithLock callback is active") {
		t.Fatalf("WithLock inner Status error = %v, want fast-fail conflict", err)
	}
}

func TestRunnerManualReviewGuardDoesNotDirty(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)
	writeMigrationFile(t, tempDir, "000001_create_users.down.sql", ManualReviewGuardStatement+`;
DROP TABLE users;`)

	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	database := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	if err := runner.Up(context.Background()); err != nil {
		t.Fatalf("Up failed: %v", err)
	}
	if err := runner.Down(context.Background()); !errors.Is(err, ErrReviewRequired) {
		t.Fatalf("Down error = %v, want ErrReviewRequired", err)
	}
	status, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status.Dirty || status.Version != 1 {
		t.Fatalf("unexpected status after guarded down: %#v", status)
	}
	assertTableExists(t, database, "users", true)
}

func TestRunnerRollsBackFailedFileWhenTransactionSupported(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_broken.up.sql", `
CREATE TABLE transient_users (id INTEGER PRIMARY KEY);
CREATE TABL broken (
`)

	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	database := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	if err := runner.Up(context.Background()); err == nil {
		t.Fatal("expected Up to fail")
	}
	status, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !status.Dirty || status.Version != 1 {
		t.Fatalf("unexpected dirty status: %#v", status)
	}
	assertTableExists(t, database, "transient_users", false)
}

func TestRunnerWithoutTransactionLeavesAppliedStatementsDirty(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_broken.up.sql", `
CREATE TABLE transient_users (id INTEGER PRIMARY KEY);
CREATE TABL broken (
`)

	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	database := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	runner, err := NewRunner(database, source, WithoutTransaction())
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	if err := runner.Up(context.Background()); err == nil {
		t.Fatal("expected Up to fail")
	}
	status, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !status.Dirty || status.Version != 1 {
		t.Fatalf("unexpected dirty status: %#v", status)
	}
	assertTableExists(t, database, "transient_users", true)
}

func TestRunnerDoesNotDirtyWhenBeginFails(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeMigrationFile(t, tempDir, "000001_create_users.up.sql", `CREATE TABLE users (id INTEGER PRIMARY KEY);`)
	source, err := NewFileSource(tempDir)
	if err != nil {
		t.Fatalf("NewFileSource failed: %v", err)
	}
	base := openSQLiteDB(t, filepath.Join(tempDir, "runner.db"))
	database := &beginFailDB{IDatabase: base}
	runner, err := NewRunner(database, source)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	if err := runner.Up(context.Background()); err == nil || !strings.Contains(err.Error(), "begin failed") {
		t.Fatalf("Up error = %v, want begin failed", err)
	}
	status, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status.Version != 0 || status.Dirty {
		t.Fatalf("unexpected status after begin failure: %#v", status)
	}
}

func writeMigrationFile(t *testing.T, dir string, name string, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) failed: %v", name, err)
	}
}

func openSQLiteDB(t *testing.T, path string) db.IDatabase {
	t.Helper()
	database, err := stdsql.NewWithContext(context.Background(), db.DBConfig{
		Driver:   "sqlite",
		Database: path,
	})
	if err != nil {
		t.Fatalf("open sqlite db failed: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	return database
}

func assertTableExists(t *testing.T, database db.IDatabase, name string, want bool) {
	t.Helper()
	row := database.QueryRow(context.Background(), "SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?", name)
	var count int
	if err := row.Scan(&count); err != nil {
		t.Fatalf("QueryRow table %s failed: %v", name, err)
	}
	got := count > 0
	if got != want {
		t.Fatalf("table %s exists = %v, want %v", name, got, want)
	}
}

type recordingDB struct {
	db.IDatabase
	statusWrites []Status
}

func (d *recordingDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	d.recordStatusWrite(query, args...)
	return d.IDatabase.Exec(ctx, query, args...)
}

func (d *recordingDB) Begin(ctx context.Context) (db.ITransaction, error) {
	tx, err := d.IDatabase.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &recordingTx{ITransaction: tx, recorder: d}, nil
}

func (d *recordingDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.ITransaction, error) {
	tx, err := d.IDatabase.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &recordingTx{ITransaction: tx, recorder: d}, nil
}

func (d *recordingDB) recordStatusWrite(query string, args ...any) {
	trimmed := strings.TrimSpace(query)
	if strings.HasPrefix(strings.TrimSpace(query), "UPDATE") &&
		strings.Contains(query, defaultStateTable) &&
		len(args) >= 4 {
		version, _ := args[0].(uint64)
		dirty, _ := args[1].(int)
		migrationType, _ := args[3].(string)
		if migrationType == lockMigrationType {
			return
		}
		d.statusWrites = append(d.statusWrites, Status{Type: migrationType, Version: version, Dirty: dirty != 0})
		return
	}
	if strings.HasPrefix(trimmed, "INSERT") &&
		strings.Contains(query, defaultStateTable) &&
		len(args) >= 4 {
		migrationType, _ := args[0].(string)
		if migrationType == lockMigrationType {
			return
		}
		version, _ := args[1].(uint64)
		dirty, _ := args[2].(int)
		d.statusWrites = append(d.statusWrites, Status{Type: migrationType, Version: version, Dirty: dirty != 0})
	}
}

type recordingTx struct {
	db.ITransaction
	recorder *recordingDB
}

func (t *recordingTx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	t.recorder.recordStatusWrite(query, args...)
	return t.ITransaction.Exec(ctx, query, args...)
}

type queryCaptureDB struct {
	db.IDatabase
	execs   []string
	queries []string
}

func (d *queryCaptureDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	d.execs = append(d.execs, query)
	return d.IDatabase.Exec(ctx, query, args...)
}

func (d *queryCaptureDB) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	d.queries = append(d.queries, query)
	return d.IDatabase.QueryRow(ctx, query, args...)
}

func (d *queryCaptureDB) hasExec(fragment string) bool {
	for _, query := range d.execs {
		if strings.Contains(query, fragment) {
			return true
		}
	}
	return false
}

func (d *queryCaptureDB) hasQuery(fragment string) bool {
	for _, query := range d.queries {
		if strings.Contains(query, fragment) {
			return true
		}
	}
	return false
}

type compatStatusRaceDB struct {
	inserts int
	updates int
}

func (d *compatStatusRaceDB) Query(context.Context, string, ...any) (db.IRows, error) {
	return nil, errors.New("unexpected query")
}

func (d *compatStatusRaceDB) QueryRow(context.Context, string, ...any) db.IRow {
	return compatCountRow{count: 0}
}

func (d *compatStatusRaceDB) Exec(_ context.Context, query string, _ ...any) (sql.Result, error) {
	trimmed := strings.TrimSpace(query)
	switch {
	case strings.HasPrefix(trimmed, "INSERT"):
		d.inserts++
		return nil, errors.New("UNIQUE constraint failed: schema_migrations.migration_type")
	case strings.HasPrefix(trimmed, "UPDATE"):
		d.updates++
		return compatResult(1), nil
	default:
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
}

func (d *compatStatusRaceDB) Begin(context.Context) (db.ITransaction, error) {
	return nil, errors.New("unexpected begin")
}

func (d *compatStatusRaceDB) BeginTx(context.Context, *sql.TxOptions) (db.ITransaction, error) {
	return nil, errors.New("unexpected begin tx")
}

func (d *compatStatusRaceDB) Ping(context.Context) error { return nil }

func (d *compatStatusRaceDB) Close() error { return nil }

type compatCountRow struct {
	count int
}

func (r compatCountRow) Scan(dest ...any) error {
	if len(dest) == 0 {
		return nil
	}
	count, ok := dest[0].(*int)
	if !ok {
		return fmt.Errorf("unexpected scan destination %T", dest[0])
	}
	*count = r.count
	return nil
}

func (r compatCountRow) Err() error { return nil }

type compatResult int64

func (r compatResult) LastInsertId() (int64, error) { return 0, nil }

func (r compatResult) RowsAffected() (int64, error) { return int64(r), nil }

type lockRefreshCaptureDB struct {
	db.IDatabase
	rootRefreshes int
	txRefreshes   int
}

func (d *lockRefreshCaptureDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if isLockRefreshQuery(query, args...) {
		d.rootRefreshes++
	}
	return d.IDatabase.Exec(ctx, query, args...)
}

func (d *lockRefreshCaptureDB) Begin(ctx context.Context) (db.ITransaction, error) {
	tx, err := d.IDatabase.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &lockRefreshCaptureTx{ITransaction: tx, recorder: d}, nil
}

func (d *lockRefreshCaptureDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.ITransaction, error) {
	tx, err := d.IDatabase.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &lockRefreshCaptureTx{ITransaction: tx, recorder: d}, nil
}

type lockRefreshCaptureTx struct {
	db.ITransaction
	recorder *lockRefreshCaptureDB
}

func (t *lockRefreshCaptureTx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if isLockRefreshQuery(query, args...) {
		t.recorder.txRefreshes++
	}
	return t.ITransaction.Exec(ctx, query, args...)
}

func isLockRefreshQuery(query string, args ...any) bool {
	trimmed := strings.TrimSpace(query)
	if !strings.HasPrefix(trimmed, "UPDATE") || !strings.Contains(query, defaultStateTable) {
		return false
	}
	if !strings.Contains(query, "updated_at = ?") || !strings.Contains(query, "version = ?") {
		return false
	}
	return len(args) == 3
}

type heartbeatFailDB struct {
	db.IDatabase
	failed chan struct{}
	once   sync.Once
}

func (d *heartbeatFailDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if isLockRefreshQuery(query, args...) {
		d.once.Do(func() { close(d.failed) })
		return nil, fmt.Errorf("heartbeat refresh failed")
	}
	return d.IDatabase.Exec(ctx, query, args...)
}

type rowsAffectedErrorDB struct {
	db.IDatabase
	err   error
	match func(query string, args ...any) bool
}

func (d *rowsAffectedErrorDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := d.IDatabase.Exec(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if d.match != nil && d.match(query, args...) {
		return rowsAffectedErrorResult{Result: result, err: d.err}, nil
	}
	return result, nil
}

func (d *rowsAffectedErrorDB) DialectName() string { return "sqlite" }

type rowsAffectedErrorResult struct {
	sql.Result
	err error
}

func (r rowsAffectedErrorResult) RowsAffected() (int64, error) {
	return 0, r.err
}

func isStaleLockReclaimQuery(query string, args ...any) bool {
	trimmed := strings.TrimSpace(query)
	if !strings.HasPrefix(trimmed, "UPDATE") || !strings.Contains(query, defaultStateTable) {
		return false
	}
	return strings.Contains(query, "updated_at <= ?") && len(args) == 5
}

type beginFailDB struct {
	db.IDatabase
}

func (d *beginFailDB) Begin(context.Context) (db.ITransaction, error) {
	return nil, fmt.Errorf("begin failed")
}

func (d *beginFailDB) BeginTx(context.Context, *sql.TxOptions) (db.ITransaction, error) {
	return nil, fmt.Errorf("begin failed")
}
