package conf

import (
	"fmt"
	"sync"

	"NetherProxy/internal/logger"
)

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
