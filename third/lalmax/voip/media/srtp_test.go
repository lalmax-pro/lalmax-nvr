package media

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSrtpKey(t *testing.T) {
	key, err := GenerateSrtpKey()
	require.NoError(t, err)
	assert.Equal(t, 30, len(key), "key material should be 30 bytes")

	// 验证随机性
	key2, err := GenerateSrtpKey()
	require.NoError(t, err)
	assert.NotEqual(t, key, key2, "keys should be random")
}

func TestBuildCryptoAttribute(t *testing.T) {
	keyMaterial := make([]byte, 30)
	for i := range keyMaterial {
		keyMaterial[i] = byte(i)
	}

	crypto := BuildCryptoAttribute(1, CryptoSuiteAES128_CM_HMAC_SHA1_80, keyMaterial)

	expected := "a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:" + base64.StdEncoding.EncodeToString(keyMaterial)
	assert.Equal(t, expected, crypto)
}

func TestParseCryptoAttribute(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(*testing.T, *CryptoParams)
	}{
		{
			name:    "valid crypto with prefix",
			input:   "a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:WVNfX19zZW1jdGwgKCkgewkyMjA7fQp9CnVubGVz",
			wantErr: false,
			check: func(t *testing.T, cp *CryptoParams) {
				assert.Equal(t, 1, cp.Tag)
				assert.Equal(t, CryptoSuiteAES128_CM_HMAC_SHA1_80, cp.Suite)
				assert.Equal(t, "inline:WVNfX19zZW1jdGwgKCkgewkyMjA7fQp9CnVubGVz", cp.KeyParams)
			},
		},
		{
			name:    "valid crypto without prefix",
			input:   "1 AES_CM_128_HMAC_SHA1_80 inline:WVNfX19zZW1jdGwgKCkgewkyMjA7fQp9CnVubGVz",
			wantErr: false,
			check: func(t *testing.T, cp *CryptoParams) {
				assert.Equal(t, 1, cp.Tag)
			},
		},
		{
			name:    "invalid format - too few parts",
			input:   "a=crypto:1 AES_CM_128_HMAC_SHA1_80",
			wantErr: true,
		},
		{
			name:    "invalid tag",
			input:   "a=crypto:abc AES_CM_128_HMAC_SHA1_80 inline:key",
			wantErr: true,
		},
		{
			name:    "unsupported suite",
			input:   "a=crypto:1 UNSUPPORTED_SUITE inline:key",
			wantErr: true,
		},
		{
			name:    "non-inline key params",
			input:   "a=crypto:1 AES_CM_128_HMAC_SHA1_80 url:http://example.com/key",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp, err := ParseCryptoAttribute(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.check != nil {
				tt.check(t, cp)
			}
		})
	}
}

func TestExtractKeyMaterial(t *testing.T) {
	// 生成30字节的测试密钥
	testKey := make([]byte, 30)
	for i := range testKey {
		testKey[i] = byte(i)
	}
	keyBase64 := base64.StdEncoding.EncodeToString(testKey)

	tests := []struct {
		name    string
		crypto  *CryptoParams
		wantErr bool
		wantKey []byte
	}{
		{
			name: "valid inline key",
			crypto: &CryptoParams{
				Tag:       1,
				Suite:     CryptoSuiteAES128_CM_HMAC_SHA1_80,
				KeyParams: "inline:" + keyBase64,
			},
			wantErr: false,
			wantKey: testKey,
		},
		{
			name: "inline key with lifetime",
			crypto: &CryptoParams{
				Tag:       1,
				Suite:     CryptoSuiteAES128_CM_HMAC_SHA1_80,
				KeyParams: "inline:" + keyBase64 + "|2^31",
			},
			wantErr: false,
			wantKey: testKey,
		},
		{
			name: "non-inline key",
			crypto: &CryptoParams{
				Tag:       1,
				Suite:     CryptoSuiteAES128_CM_HMAC_SHA1_80,
				KeyParams: "url:http://example.com/key",
			},
			wantErr: true,
		},
		{
			name: "invalid base64",
			crypto: &CryptoParams{
				Tag:       1,
				Suite:     CryptoSuiteAES128_CM_HMAC_SHA1_80,
				KeyParams: "inline:!!!invalid!!!",
			},
			wantErr: true,
		},
		{
			name: "wrong key length",
			crypto: &CryptoParams{
				Tag:       1,
				Suite:     CryptoSuiteAES128_CM_HMAC_SHA1_80,
				KeyParams: "inline:" + base64.StdEncoding.EncodeToString([]byte("short")),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := ExtractKeyMaterial(tt.crypto)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantKey, key)
		})
	}
}

func TestCreateSrtpContext(t *testing.T) {
	keyMaterial, err := GenerateSrtpKey()
	require.NoError(t, err)

	ctx, err := CreateSrtpContext(keyMaterial, CryptoSuiteAES128_CM_HMAC_SHA1_80)
	require.NoError(t, err)
	assert.NotNil(t, ctx)
	assert.NotNil(t, ctx.inboundRTPContext)
	assert.NotNil(t, ctx.inboundRTCPContext)
	assert.NotNil(t, ctx.outboundRTPContext)
	assert.NotNil(t, ctx.outboundRTCPContext)
	defer ctx.Close()

	// 测试错误情况
	_, err = CreateSrtpContext([]byte("short"), CryptoSuiteAES128_CM_HMAC_SHA1_80)
	assert.Error(t, err)

	_, err = CreateSrtpContext(keyMaterial, "INVALID_SUITE")
	assert.Error(t, err)
}

func TestSrtpEncryptDecrypt(t *testing.T) {
	keyMaterial, err := GenerateSrtpKey()
	require.NoError(t, err)

	ctx, err := CreateSrtpContext(keyMaterial, CryptoSuiteAES128_CM_HMAC_SHA1_80)
	require.NoError(t, err)
	defer ctx.Close()

	// 构造一个简单的RTP包
	rtpPacket := []byte{
		0x80, 0x60, 0x00, 0x01, // V=2, P=0, X=0, CC=0, M=0, PT=96, Seq=1
		0x00, 0x00, 0x00, 0x01, // Timestamp=1
		0x12, 0x34, 0x56, 0x78, // SSRC=0x12345678
		0xAA, 0xBB, 0xCC, 0xDD, // Payload
	}

	// 加密
	encrypted, err := ctx.EncryptRTP(rtpPacket)
	require.NoError(t, err)
	assert.NotEqual(t, rtpPacket, encrypted)
	assert.Greater(t, len(encrypted), len(rtpPacket), "encrypted packet should be larger")

	// 解密
	decrypted, err := ctx.DecryptRTP(encrypted)
	require.NoError(t, err)
	assert.Equal(t, rtpPacket, decrypted)
}

func TestSrtpContextPairUsesDirectionalKeys(t *testing.T) {
	localKey, err := GenerateSrtpKey()
	require.NoError(t, err)
	remoteKey, err := GenerateSrtpKey()
	require.NoError(t, err)

	serverCtx, err := CreateSrtpContextPair(localKey, remoteKey, CryptoSuiteAES128_CM_HMAC_SHA1_80)
	require.NoError(t, err)
	defer serverCtx.Close()
	remoteCtx, err := CreateSrtpContextPair(remoteKey, localKey, CryptoSuiteAES128_CM_HMAC_SHA1_80)
	require.NoError(t, err)
	defer remoteCtx.Close()

	rtpPacket := []byte{
		0x80, 0x60, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x01,
		0x12, 0x34, 0x56, 0x78,
		0xAA, 0xBB, 0xCC, 0xDD,
	}

	fromRemote, err := remoteCtx.EncryptRTP(rtpPacket)
	require.NoError(t, err)
	decryptedByServer, err := serverCtx.DecryptRTP(fromRemote)
	require.NoError(t, err)
	assert.Equal(t, rtpPacket, decryptedByServer)

	fromServer, err := serverCtx.EncryptRTP(rtpPacket)
	require.NoError(t, err)
	decryptedByRemote, err := remoteCtx.DecryptRTP(fromServer)
	require.NoError(t, err)
	assert.Equal(t, rtpPacket, decryptedByRemote)
}

func TestSrtpContextClose(t *testing.T) {
	keyMaterial, err := GenerateSrtpKey()
	require.NoError(t, err)

	ctx, err := CreateSrtpContext(keyMaterial, CryptoSuiteAES128_CM_HMAC_SHA1_80)
	require.NoError(t, err)

	ctx.Close()

	// 验证密钥材料已被清除
	for _, b := range ctx.keyMaterial {
		assert.Equal(t, byte(0), b, "key material should be zeroed")
	}
}

func TestSrtpContextRetainsRolloverAcrossMediaReplacement(t *testing.T) {
	key, err := GenerateSrtpKey()
	require.NoError(t, err)
	incoming, err := CreateSrtpContext(key, CryptoSuiteAES128_CM_HMAC_SHA1_80)
	require.NoError(t, err)
	peer, err := CreateSrtpContext(key, CryptoSuiteAES128_CM_HMAC_SHA1_80)
	require.NoError(t, err)
	defer peer.Close()
	packet := []byte{0x80, 0x60, 0xff, 0xff, 0, 0, 0, 1, 0x12, 0x34, 0x56, 0x78, 0xaa}
	encrypted, err := peer.EncryptRTP(packet)
	require.NoError(t, err)
	_, err = incoming.DecryptRTP(encrypted)
	require.NoError(t, err)
	retained := incoming.Retain()
	require.NotNil(t, retained)
	incoming.Close()
	packet[2] = 0
	packet[3] = 0
	packet[7] = 2
	encrypted, err = peer.EncryptRTP(packet)
	require.NoError(t, err)
	plain, err := retained.DecryptRTP(encrypted)
	require.NoError(t, err)
	require.Equal(t, packet, plain)
	retained.Close()
	require.Nil(t, retained.Retain())
	_, err = retained.DecryptRTP(encrypted)
	require.Error(t, err)
}
