package middleware

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLogFileLine_SlogAndGIN(t *testing.T) {
	slogLine := `time=2026-09-22T15:21:18.438+08:00 level=INFO msg=request method=GET path=/api/stats`
	entry := ParseLogFileLine(slogLine, 10)
	require.Equal(t, "INFO", entry.Level)
	require.Equal(t, "request", entry.Message)
	require.Equal(t, "GET", entry.Attrs["method"])
	require.Equal(t, uint64(10), entry.Seq)

	quoted := `time=2026-09-22T15:21:18.438+08:00 level=WARN msg="lalmax-nvr listening" addr=:9090`
	entry = ParseLogFileLine(quoted, 1)
	require.Equal(t, "WARN", entry.Level)
	require.Equal(t, "lalmax-nvr listening", entry.Message)

	ginLine := `[GIN] 2026/09/22 - 15:20:48 | 500 | 1ms | 127.0.0.1 | GET "/x"`
	entry = ParseLogFileLine(ginLine, 2)
	require.Equal(t, "ERROR", entry.Level)
	require.Equal(t, ginLine, entry.Message)
}

func TestReadLogFileTail_FiltersLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lalmax-nvr.log")
	body := "" +
		"time=2026-09-22T15:21:18.438+08:00 level=INFO msg=started\n" +
		"time=2026-09-22T15:21:19.438+08:00 level=ERROR msg=boom camera_id=cam-1\n" +
		"[GIN] 2026/09/22 - 15:21:20 | 200 | 1ms | 127.0.0.1 | GET \"/api/stat/all_group\"\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	logs, _, err := ReadLogFileTail(path, LogRingFilter{MinLevel: ParseLogLevel("error"), Limit: 20})
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Equal(t, "boom", logs[0].Message)

	logs, _, err = ReadLogFileTail(path, LogRingFilter{Query: "all_group", Limit: 20})
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Contains(t, logs[0].Message, "all_group")
}
