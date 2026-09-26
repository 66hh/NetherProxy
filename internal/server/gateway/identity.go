package gateway

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"NetherProxy/internal/logger"
)

// jwksURL 是微软 Minecraft 服务的签名公钥端点
const jwksURL = "https://authorization.franchise.minecraft-services.net/.well-known/keys"

// identityIssuer 是合法 identity token 的签发者
const identityIssuer = "https://authorization.franchise.minecraft-services.net/"

// errIdentityRejected 对应 BDS 的数字错误码 37 (IdentityNotAllowed)
var errIdentityRejected = errors.New("identity not allowed")

// identityVerifier 验证 offer 中 a=identity 携带的微软签发玩家身份 JWT,
// 公钥从 JWKS 端点获取并缓存, kid 未命中时自动刷新
type identityVerifier struct {
	client *http.Client

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	lastFetch time.Time
	fetchMu   sync.Mutex // 串行化 JWKS 拉取 (锁外网络请求)
}

func newIdentityVerifier() *identityVerifier {
	v := &identityVerifier{
		client: &http.Client{Timeout: 10 * time.Second},
		keys:   make(map[string]*rsa.PublicKey),
	}
	// 初始拉取放后台: 不阻塞启动, 失败按退避自动重试
	go v.fetchKeys()
	return v
}

// PlayerInfo 是从验证通过的 identity 中提取的玩家信息
type PlayerInfo struct {
	Name string // xname, 玩家名
	XUID string // xid, Xbox 用户 ID
}

// verify 提取并验证 offer SDP 中的 identity, 返回玩家信息
func (v *identityVerifier) verify(offer []byte) (PlayerInfo, error) {
	token, err := extractIdentityToken(offer)
	if err != nil {
		return PlayerInfo{}, err
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return PlayerInfo{}, fmt.Errorf("%w: malformed jwt", errIdentityRejected)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return PlayerInfo{}, fmt.Errorf("%w: bad jwt header", errIdentityRejected)
	}
	if header.Alg != "RS256" {
		return PlayerInfo{}, fmt.Errorf("%w: unexpected alg %q", errIdentityRejected, header.Alg)
	}

	key, err := v.publicKey(header.Kid)
	if err != nil {
		return PlayerInfo{}, err
	}

	signed := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return PlayerInfo{}, fmt.Errorf("%w: bad jwt signature encoding", errIdentityRejected)
	}
	digest := sha256.Sum256([]byte(signed))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return PlayerInfo{}, fmt.Errorf("%w: invalid signature", errIdentityRejected)
	}

	var claims struct {
		Iss   string `json:"iss"`
		Exp   int64  `json:"exp"`
		XName string `json:"xname"`
		XID   string `json:"xid"`
	}
	if err := decodeSegment(parts[1], &claims); err != nil {
		return PlayerInfo{}, fmt.Errorf("%w: bad jwt claims", errIdentityRejected)
	}
	if claims.Iss != identityIssuer {
		return PlayerInfo{}, fmt.Errorf("%w: unexpected issuer", errIdentityRejected)
	}
	// 60s 时钟偏差容忍
	if time.Now().Unix() > claims.Exp+60 {
		return PlayerInfo{}, fmt.Errorf("%w: token expired", errIdentityRejected)
	}
	if claims.XName == "" {
		return PlayerInfo{}, fmt.Errorf("%w: missing xname", errIdentityRejected)
	}
	if claims.XID == "" {
		return PlayerInfo{}, fmt.Errorf("%w: missing xid", errIdentityRejected)
	}
	return PlayerInfo{Name: claims.XName, XUID: claims.XID}, nil
}

// extractPlayer 仅解析 identity 中的玩家信息, 不验签不校验时效
// 用于 verify_identity 关闭时绑定会话展示, 信息不可信仅作参考
func (v *identityVerifier) extractPlayer(offer []byte) PlayerInfo {
	token, err := extractIdentityToken(offer)
	if err != nil {
		return PlayerInfo{}
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return PlayerInfo{}
	}
	var claims struct {
		XName string `json:"xname"`
		XID   string `json:"xid"`
	}
	if err := decodeSegment(parts[1], &claims); err != nil {
		return PlayerInfo{}
	}
	return PlayerInfo{Name: claims.XName, XUID: claims.XID}
}

// extractIdentityToken 从 offer SDP 中提取 identity 断言里的玩家身份 token
func extractIdentityToken(offer []byte) (string, error) {
	for _, line := range strings.Split(strings.ReplaceAll(string(offer), "\r\n", "\n"), "\n") {
		v, ok := strings.CutPrefix(line, "a=identity:")
		if !ok {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(v))
		if err != nil {
			return "", fmt.Errorf("%w: bad identity encoding", errIdentityRejected)
		}
		var outer struct {
			Assertion string `json:"assertion"`
		}
		if err := json.Unmarshal(raw, &outer); err != nil {
			return "", fmt.Errorf("%w: bad identity json", errIdentityRejected)
		}
		var assertion struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(outer.Assertion), &assertion); err != nil || assertion.Token == "" {
			return "", fmt.Errorf("%w: bad identity assertion", errIdentityRejected)
		}
		return assertion.Token, nil
	}
	return "", fmt.Errorf("%w: missing identity", errIdentityRejected)
}

// publicKey 按 kid 取公钥, 未命中时刷新 JWKS 重试一次。
// 网络拉取在锁外进行 (singleflight), 不阻塞其他验签;
// 缓存为空 (启动失败) 时用短退避, 否则 5 分钟内不重复拉取。
func (v *identityVerifier) publicKey(kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	key, ok := v.keys[kid]
	empty := len(v.keys) == 0
	v.mu.RUnlock()
	if ok {
		return key, nil
	}

	backoff := 5 * time.Minute
	if empty {
		backoff = 10 * time.Second
	}
	v.fetchMu.Lock()
	if time.Since(v.lastFetch) >= backoff {
		v.fetchKeys() // 内部持写锁, 失败也更新 lastFetch
	}
	v.fetchMu.Unlock()

	v.mu.RLock()
	defer v.mu.RUnlock()
	if key, ok := v.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("%w: unknown signing key", errIdentityRejected)
}

// fetchKeys 拉取 JWKS: 网络请求在锁外执行, 仅在替换缓存时短暂持锁
func (v *identityVerifier) fetchKeys() error {
	resp, err := v.client.Get(jwksURL)
	if err != nil {
		v.mu.Lock()
		v.lastFetch = time.Now()
		v.mu.Unlock()
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		v.mu.Lock()
		v.lastFetch = time.Now()
		v.mu.Unlock()
		return fmt.Errorf("jwks returned %d", resp.StatusCode)
	}
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Use string `json:"use"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		v.mu.Lock()
		v.lastFetch = time.Now()
		v.mu.Unlock()
		return err
	}
	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" || k.Use != "sig" {
			continue
		}
		nb, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nb),
			E: int(new(big.Int).SetBytes(eb).Int64()),
		}
	}
	if len(keys) == 0 {
		v.mu.Lock()
		v.lastFetch = time.Now()
		v.mu.Unlock()
		return errors.New("jwks contains no usable RSA keys")
	}
	v.mu.Lock()
	v.keys = keys
	v.lastFetch = time.Now()
	v.mu.Unlock()
	logger.Info("jwks refreshed", "keys", len(keys))
	return nil
}

// decodeSegment 解码 JWT 的 base64url 段为 JSON
func decodeSegment(seg string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
