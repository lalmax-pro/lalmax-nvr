package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"

	"github.com/stretchr/testify/require"
)

func TestOpenExternalDriverConnections(t *testing.T) {
	tests := []struct {
		name   string
		driver DatabaseDriver
		env    string
	}{
		{name: "postgres", driver: DriverPostgres, env: "LALMAX_TEST_POSTGRES_DSN"},
		{name: "mysql", driver: DriverMySQL, env: "LALMAX_TEST_MYSQL_DSN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsn := os.Getenv(tt.env)
			if dsn == "" {
				t.Skipf("set %s to test the %s driver", tt.env, tt.name)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			db, err := Open(ctx, DatabaseOptions{Driver: tt.driver, DSN: dsn, MaxOpenConns: 2, MaxIdleConns: 1})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			require.NoError(t, db.Ping(ctx))
		})
	}
}

func TestExternalDriverSchemaInitialization(t *testing.T) {
	tests := []struct {
		name   string
		driver DatabaseDriver
		env    string
	}{
		{name: "postgres", driver: DriverPostgres, env: "LALMAX_TEST_POSTGRES_DSN"},
		{name: "mysql", driver: DriverMySQL, env: "LALMAX_TEST_MYSQL_DSN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsn := os.Getenv(tt.env)
			if dsn == "" {
				t.Skipf("set %s to test %s schema initialization", tt.env, tt.name)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			db, err := Open(ctx, DatabaseOptions{Driver: tt.driver, DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 2})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			require.NoError(t, db.Init(ctx))
			runCameraRecordingRepositoryContract(t, db, db)
			runFeatureAndVoIPRepositoryContract(t, db)
			runGeneratedIDRepositoryContract(t, db)
		})
	}
}

func TestSQLiteMigrationToExternalDriver(t *testing.T) {
	tests := []struct {
		name   string
		driver DatabaseDriver
		env    string
	}{
		{name: "postgres", driver: DriverPostgres, env: "LALMAX_TEST_POSTGRES_MIGRATION_DSN"},
		{name: "mysql", driver: DriverMySQL, env: "LALMAX_TEST_MYSQL_MIGRATION_DSN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsn := os.Getenv(tt.env)
			if dsn == "" {
				t.Skipf("set %s to test SQLite migration to %s", tt.env, tt.name)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			db, err := New(filepath.Join(t.TempDir(), "migration-source.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			require.NoError(t, db.Init(ctx))
			runCameraRecordingRepositoryContract(t, db, db)
			runFeatureAndVoIPRepositoryContract(t, db)
			runGeneratedIDRepositoryContract(t, db)

			_, err = db.MigrateTo(ctx, tt.driver, dsn, func() error { return errors.New("config save failed") })
			require.Error(t, err)
			require.Equal(t, DriverSQLite, db.driver, "failed config persistence must leave the source active")

			configPersisted := false
			summary, err := db.MigrateTo(ctx, tt.driver, dsn, func() error {
				configPersisted = true
				return nil
			})
			require.NoError(t, err)
			require.True(t, configPersisted)
			require.Equal(t, tt.driver, db.driver)
			require.Greater(t, summary.TotalRows, int64(0))
			count, err := db.CountRecordings(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, count)
			camera, err := db.GetCamera(ctx, "contract-camera")
			require.NoError(t, err)
			require.NotNil(t, camera)
			require.Equal(t, "Contract camera", camera.Name)
			calls, totalCalls, err := db.ListVoIPCalls(ctx, 10, 0)
			require.NoError(t, err)
			require.GreaterOrEqual(t, totalCalls, 1)
			require.NotEmpty(t, calls)

			postCutoverUser := &model.User{Username: "post-cutover-" + time.Now().Format("150405.000000000"), PasswordHash: "hash", Role: model.RoleUser, Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			require.NoError(t, db.CreateUser(ctx, postCutoverUser))
			require.Positive(t, postCutoverUser.ID)

			backupPath := filepath.Join(t.TempDir(), "post-cutover-backup.db")
			require.NoError(t, db.Backup(ctx, backupPath))
			backupDB, err := New(backupPath)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, backupDB.Close()) })
			backupCount, err := backupDB.CountRecordings(ctx)
			require.NoError(t, err)
			require.Equal(t, count, backupCount)
		})
	}
}

func runGeneratedIDRepositoryContract(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000000")

	user := &model.User{Username: "contract-user-" + suffix, PasswordHash: "hash", Role: model.RoleUser, Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	require.NoError(t, db.CreateUser(ctx, user))
	require.Positive(t, user.ID)
	gotUser, err := db.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, user.Username, gotUser.Username)

	groupID, err := db.CreateDeviceGroup(ctx, &DeviceGroup{Name: "contract-group-" + suffix})
	require.NoError(t, err)
	require.Positive(t, groupID)

	history := &StreamHistory{StreamID: "contract-stream-" + suffix, SessionID: "contract-session-" + suffix, StartedAt: time.Now().UTC()}
	require.NoError(t, db.InsertStreamHistory(ctx, history))
	require.Positive(t, history.ID)

	ban := &StreamBan{StreamID: "contract-banned-" + suffix, Reason: "test", CreatedAt: time.Now().UTC()}
	require.NoError(t, db.InsertStreamBan(ctx, ban))
	require.Positive(t, ban.ID)

	regionID, err := db.CreateGBRegion(ctx, &GBRegion{DeviceID: "contract-region-" + suffix, Name: "Contract region"})
	require.NoError(t, err)
	require.Positive(t, regionID)

	gbGroupID, err := db.CreateGBGroup(ctx, &GBGroup{DeviceID: "contract-gb-group-" + suffix, Name: "Contract GB group", BusinessGroup: "contract-business-" + suffix})
	require.NoError(t, err)
	require.Positive(t, gbGroupID)
	gbDevice := &GB28181DeviceRow{DeviceID: "contract-device-" + suffix, Name: "Contract device", IsOnline: true}
	require.NoError(t, db.UpsertGB28181Device(ctx, gbDevice))
	require.NoError(t, db.UpdateGB28181DeviceStatus(ctx, gbDevice.DeviceID, true, "127.0.0.1"))
	require.NoError(t, db.UpdateGB28181DeviceOnlineStatus(ctx, gbDevice.DeviceID, false))

	plan := &RecordingPlan{ID: "contract-plan-" + suffix, StreamID: "contract-plan-stream-" + suffix, Enabled: true, Mode: RecordingModeContinuous}
	require.NoError(t, db.UpsertRecordingPlan(ctx, plan))
	gotPlan, err := db.GetRecordingPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.NotNil(t, gotPlan)
	require.True(t, gotPlan.Enabled)

	platformID, err := db.CreatePlatform(ctx, &PlatformRow{Name: "contract-platform-" + suffix, Enable: true, ServerGBID: "34020000002000000001", DeviceGBID: "34020000001320000001"})
	require.NoError(t, err)
	require.Positive(t, platformID)

	_, err = db.CreateDownload(ctx, &DownloadRecordRow{DeviceID: "contract-device", ChannelID: "contract-channel", Status: "pending", StartTime: time.Now().UTC(), EndTime: time.Now().UTC().Add(time.Minute)})
	require.NoError(t, err)

	_, err = db.CreateAlarm(ctx, &AlarmRow{DeviceID: "contract-device", AlarmTime: time.Now().UTC()})
	require.NoError(t, err)

	_, err = db.InsertEvent(ctx, model.Event{CameraID: "contract-camera", Source: model.EventSourceHealth, Type: "contract", Message: "test"})
	require.NoError(t, err)

	rule := &model.AlarmRule{Name: "contract-rule-" + suffix, Enabled: true, Action: model.AlarmActionRecord}
	require.NoError(t, db.InsertAlarmRule(ctx, rule))
	require.Positive(t, rule.ID)

	_, err = db.InsertOperationLog(ctx, model.OperationLog{Action: "contract", Resource: "database", Status: "success"})
	require.NoError(t, err)
}

func runFeatureAndVoIPRepositoryContract(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	flagKey := "contract-feature-" + time.Now().Format("150405.000000000")
	require.NoError(t, db.SetFeatureFlag(ctx, flagKey, true))
	got, err := db.GetFeatureFlag(ctx, flagKey, false)
	require.NoError(t, err)
	require.True(t, got)

	call := VoIPCall{
		CallID:    "contract-call-" + time.Now().Format("150405.000000000"),
		Direction: "outbound", FromUser: "nvr", ToUser: "1001", Outcome: "completed",
		StartedAt: time.Now().UTC().Add(-time.Minute), AnsweredAt: time.Now().UTC().Add(-50 * time.Second),
		EndedAt: time.Now().UTC(), DurationSecond: 50, Transport: "tcp", AudioCodec: "PCMU",
	}
	require.NoError(t, db.SaveVoIPCall(ctx, call))
	calls, total, err := db.ListVoIPCalls(ctx, 10, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, total, 1)
	require.NotEmpty(t, calls)
	require.Equal(t, call.CallID, calls[0].CallID)
}
