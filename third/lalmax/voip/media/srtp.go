package media

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/pion/srtp/v3"
)

// SRTP加密套件定义 (RFC 4568)
const (
	CryptoSuiteAES128_CM_HMAC_SHA1_80 = "AES_CM_128_HMAC_SHA1_80"
	CryptoSuiteAES128_CM_HMAC_SHA1_32 = "AES_CM_128_HMAC_SHA1_32"
)

// CryptoParams 表示SDES crypto参数
type CryptoParams struct {
	Tag           int    // crypto标签（1-9）
	Suite         string // 加密套件
	KeyParams     string // 密钥参数 (inline:base64key)
	SessionParams string // 可选的会话参数
}

// SrtpContext SRTP加密上下文
type SrtpContext struct {
	mu                  sync.Mutex
	references          int
	inboundRTPContext   *srtp.Context
	inboundRTCPContext  *srtp.Context
	outboundRTPContext  *srtp.Context
	outboundRTCPContext *srtp.Context
	keyMaterial         []byte
	suite               string
}

// GenerateSrtpKey 生成SRTP密钥材料
// AES_CM_128_HMAC_SHA1_80: 16字节master key + 14字节salt = 30字节
func GenerateSrtpKey() ([]byte, error) {
	keyMaterial := make([]byte, 30) // 16 + 14
	if _, err := rand.Read(keyMaterial); err != nil {
		return nil, fmt.Errorf("generate srtp key failed: %w", err)
	}
	return keyMaterial, nil
}

// BuildCryptoAttribute 构建a=crypto SDP属性
// 格式: a=crypto:<tag> <crypto-suite> <key-params> [<session-params>]
// 示例: a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:WVNfX19zZW1jdGwgKCkgewkyMjA7fQp9CnVubGVz
func BuildCryptoAttribute(tag int, suite string, keyMaterial []byte) string {
	keyBase64 := base64.StdEncoding.EncodeToString(keyMaterial)
	return fmt.Sprintf("a=crypto:%d %s inline:%s", tag, suite, keyBase64)
}

// ParseCryptoAttribute 解析a=crypto SDP属性
func ParseCryptoAttribute(line string) (*CryptoParams, error) {
	// 去除 "a=crypto:" 前缀
	if !strings.HasPrefix(line, "a=crypto:") {
		line = "a=crypto:" + line
	}
	line = strings.TrimPrefix(line, "a=crypto:")

	parts := strings.Fields(line)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid crypto attribute format")
	}

	tag, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid crypto tag: %w", err)
	}

	suite := parts[1]
	keyParams := parts[2]

	// 验证suite
	if suite != CryptoSuiteAES128_CM_HMAC_SHA1_80 && suite != CryptoSuiteAES128_CM_HMAC_SHA1_32 {
		return nil, fmt.Errorf("unsupported crypto suite: %s", suite)
	}

	// 验证keyParams格式
	if !strings.HasPrefix(keyParams, "inline:") {
		return nil, fmt.Errorf("only inline key params supported")
	}

	var sessionParams string
	if len(parts) > 3 {
		sessionParams = strings.Join(parts[3:], " ")
	}

	return &CryptoParams{
		Tag:           tag,
		Suite:         suite,
		KeyParams:     keyParams,
		SessionParams: sessionParams,
	}, nil
}

// ExtractKeyMaterial 从crypto参数中提取密钥材料
func ExtractKeyMaterial(cryptoParams *CryptoParams) ([]byte, error) {
	// 解析 inline:base64key
	keyParams := cryptoParams.KeyParams
	if !strings.HasPrefix(keyParams, "inline:") {
		return nil, fmt.Errorf("only inline key params supported")
	}

	keyBase64 := strings.TrimPrefix(keyParams, "inline:")

	// 可能包含生命周期参数，如 inline:base64key|2^31
	if idx := strings.Index(keyBase64, "|"); idx != -1 {
		keyBase64 = keyBase64[:idx]
	}

	keyMaterial, err := base64.StdEncoding.DecodeString(keyBase64)
	if err != nil {
		return nil, fmt.Errorf("decode key material failed: %w", err)
	}

	// 验证密钥长度
	if len(keyMaterial) != 30 {
		return nil, fmt.Errorf("invalid key material length: %d, expected 30", len(keyMaterial))
	}

	return keyMaterial, nil
}

// CreateSrtpContext 创建SRTP上下文
func CreateSrtpContext(keyMaterial []byte, suite string) (*SrtpContext, error) {
	return CreateSrtpContextPair(keyMaterial, keyMaterial, suite)
}

// CreateSrtpContextPair 创建双向SRTP上下文。
// SDES-SRTP 的offer和answer分别携带不同密钥：remoteKeyMaterial用于解密对端媒体，
// localKeyMaterial用于加密本端发送的媒体/RTCP。
func CreateSrtpContextPair(localKeyMaterial, remoteKeyMaterial []byte, suite string) (*SrtpContext, error) {
	inboundRTP, inboundRTCP, err := createSrtpContexts(remoteKeyMaterial, suite)
	if err != nil {
		return nil, fmt.Errorf("create inbound srtp context failed: %w", err)
	}

	outboundRTP, outboundRTCP, err := createSrtpContexts(localKeyMaterial, suite)
	if err != nil {
		return nil, fmt.Errorf("create outbound srtp context failed: %w", err)
	}

	return &SrtpContext{
		references:          1,
		inboundRTPContext:   inboundRTP,
		inboundRTCPContext:  inboundRTCP,
		outboundRTPContext:  outboundRTP,
		outboundRTCPContext: outboundRTCP,
		keyMaterial:         append([]byte(nil), localKeyMaterial...),
		suite:               suite,
	}, nil
}

func createSrtpContexts(keyMaterial []byte, suite string) (*srtp.Context, *srtp.Context, error) {
	if len(keyMaterial) != 30 {
		return nil, nil, fmt.Errorf("invalid key material length: %d", len(keyMaterial))
	}

	masterKey := keyMaterial[:16]
	masterSalt := keyMaterial[16:30]

	var profile srtp.ProtectionProfile
	switch suite {
	case CryptoSuiteAES128_CM_HMAC_SHA1_80:
		profile = srtp.ProtectionProfileAes128CmHmacSha1_80
	case CryptoSuiteAES128_CM_HMAC_SHA1_32:
		profile = srtp.ProtectionProfileAes128CmHmacSha1_32
	default:
		return nil, nil, fmt.Errorf("unsupported crypto suite: %s", suite)
	}

	return createSrtpContextsFromKeySalt(masterKey, masterSalt, profile)
}

func createSrtpContextsFromKeySalt(masterKey, masterSalt []byte, profile srtp.ProtectionProfile) (*srtp.Context, *srtp.Context, error) {
	rtpCtx, err := srtp.CreateContext(masterKey, masterSalt, profile)
	if err != nil {
		return nil, nil, fmt.Errorf("create rtp context failed: %w", err)
	}

	rtcpCtx, err := srtp.CreateContext(masterKey, masterSalt, profile)
	if err != nil {
		return nil, nil, fmt.Errorf("create rtcp context failed: %w", err)
	}

	return rtpCtx, rtcpCtx, nil
}

// DecryptRTP 解密RTP包
func (ctx *SrtpContext) DecryptRTP(encrypted []byte) ([]byte, error) {
	if ctx != nil {
		ctx.mu.Lock()
		defer ctx.mu.Unlock()
	}
	if ctx == nil || ctx.inboundRTPContext == nil {
		return nil, fmt.Errorf("srtp context not initialized")
	}
	return ctx.inboundRTPContext.DecryptRTP(nil, encrypted, nil)
}

// EncryptRTP 加密RTP包
func (ctx *SrtpContext) EncryptRTP(plain []byte) ([]byte, error) {
	if ctx != nil {
		ctx.mu.Lock()
		defer ctx.mu.Unlock()
	}
	if ctx == nil || ctx.outboundRTPContext == nil {
		return nil, fmt.Errorf("srtp context not initialized")
	}
	return ctx.outboundRTPContext.EncryptRTP(nil, plain, nil)
}

// DecryptRTCP 解密RTCP包
func (ctx *SrtpContext) DecryptRTCP(encrypted []byte) ([]byte, error) {
	if ctx != nil {
		ctx.mu.Lock()
		defer ctx.mu.Unlock()
	}
	if ctx == nil || ctx.inboundRTCPContext == nil {
		return nil, fmt.Errorf("srtp context not initialized")
	}
	return ctx.inboundRTCPContext.DecryptRTCP(nil, encrypted, nil)
}

// EncryptRTCP 加密RTCP包
func (ctx *SrtpContext) EncryptRTCP(plain []byte) ([]byte, error) {
	if ctx != nil {
		ctx.mu.Lock()
		defer ctx.mu.Unlock()
	}
	if ctx == nil || ctx.outboundRTCPContext == nil {
		return nil, fmt.Errorf("srtp context not initialized")
	}
	return ctx.outboundRTCPContext.EncryptRTCP(nil, plain, nil)
}

// Retain preserves replay counters and keys when an authenticated DTLS peer
// keeps its association across an SDP update.
func (ctx *SrtpContext) Retain() *SrtpContext {
	if ctx == nil {
		return nil
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if ctx.references <= 0 {
		return nil
	}
	ctx.references++
	return ctx
}

// Close releases one owner and clears keys after the final owner leaves.
func (ctx *SrtpContext) Close() {
	if ctx == nil {
		return
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if ctx.references <= 0 {
		return
	}
	ctx.references--
	if ctx.references > 0 {
		return
	}
	ctx.inboundRTPContext = nil
	ctx.inboundRTCPContext = nil
	ctx.outboundRTPContext = nil
	ctx.outboundRTCPContext = nil
	// 清除敏感数据
	if ctx.keyMaterial != nil {
		for i := range ctx.keyMaterial {
			ctx.keyMaterial[i] = 0
		}
	}
}
