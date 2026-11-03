package xiaomi

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStepMotorCommands(t *testing.T) {
	for _, model := range []string{ModelCW400, ModelCW500} {
		t.Run(model, func(t *testing.T) {
			client, conn := newTestMISSClient()
			client.model = model
			for _, tc := range []struct {
				direction string
				operation int
			}{
				{"left", 1}, {"right", 2}, {"up", 3}, {"down", 4},
			} {
				require.NoError(t, client.MotorControlContext(context.Background(), tc.direction, 5))
				cmd, payload, ok := conn.lastWrittenCmd()
				require.True(t, ok)
				require.Equal(t, uint32(missCmdEncoded), cmd)
				decoded, err := Decode(payload, client.key)
				require.NoError(t, err)
				require.Equal(t, uint32(missCmdMotorReq), binary.BigEndian.Uint32(decoded))
				var body map[string]int
				require.NoError(t, json.Unmarshal(decoded[4:], &body))
				require.Equal(t, map[string]int{"operation": tc.operation}, body)
			}
			require.NoError(t, client.MotorControlContext(context.Background(), "stop", 0))
			require.Len(t, conn.writtenCmds, 4)
		})
	}
}
