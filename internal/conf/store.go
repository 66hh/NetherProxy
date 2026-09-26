package conf

import (
	"fmt"
	"sync"

	"NetherProxy/internal/logger"
)

// MaskedToken 是 token 在读取接口中的脱敏占位值
const MaskedToken = "***"

// Store 持有当前生效配置, 支持并发读取与原子替换 (reload/write)
type Store struct {
	mu      sync.RWMutex
	writeMu sync.Mutex // Reload/Write 串行化, 防止并发写交错
	path    string
	cfg     *Conf
}

func NewStore(path string, cfg *Conf) *Store {
	return &Store{path: path, cfg: cfg}
}

// Get 返回当前生效配置, 调用方不得修改返回值
func (s *Store) Get() *Conf {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Path 返回配置文件路径
func (s *Store) Path() string {
	return s.path
}

// Reload 从配置文件重新加载, 加载或校验失败时保留当前配置不变
func (s *Store) Reload() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	old := s.Get()
	cfg, err := Load(s.path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	// 配置文件缺少 token 字段时 Default 会重新随机生成, 提示凭据已轮换
	if cfg.Gateway.Token != old.Gateway.Token {
		logger.Warn("gateway token changed after reload, previously distributed credentials are now invalid")
	}
	return nil
}

// Write 校验并保存配置到文件, 保存成功后立即生效; 任一环节失败当前配置不变
func (s *Store) Write(cfg *Conf) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeLocked(cfg)
}

// WriteMasked 处理面板/API 写入: 脱敏 token 回填 + 缺省值填充 +
// 校验 + 落盘 + 生效, 全程持写锁原子完成, 返回写入前的旧配置
func (s *Store) WriteMasked(cfg *Conf) (old *Conf, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	old = s.Get()
	if cfg.Gateway.Token == MaskedToken {
		cfg.Gateway.Token = old.Gateway.Token
	}
	if err := s.writeLocked(cfg); err != nil {
		return nil, err
	}
	return old, nil
}

// writeLocked 校验并写入。调用者须持有 writeMu。
func (s *Store) writeLocked(cfg *Conf) error {
	normalize(cfg)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	if err := Save(s.path, cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}
