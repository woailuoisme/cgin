// Package hasher 提供基于 Argon2id 的口令安全哈希与校验（PHC 格式）。
package hasher

import (
	"cmp"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/samber/lo"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// MaxPasswordLength 口令最大允许长度（1024 字节），防御恶意超长字符串 DoS。
const MaxPasswordLength = 1024

// 哈希格式解析与校验错误。
var (
	ErrInvalidEncodedHash  = errors.New("hasher: invalid encoded hash format")
	ErrIncompatibleVersion = errors.New("hasher: incompatible argon2 version")
	ErrPasswordTooLong     = errors.New("hasher: password exceeds maximum length of 1024 bytes")
)

// PasswordHasher 定义口令安全哈希、校验与渐进式重算升级的统一契约。
type PasswordHasher interface {
	// Make 将明文口令哈希为安全编码字符串。
	Make(password string) (string, error)
	// Check 校验明文口令是否与给定的编码串匹配。
	Check(encoded, password string) (bool, error)
	// NeedsRehash 检测给定编码串是否需要升级（因参数变更或老旧算法迁移）。
	NeedsRehash(encoded string) bool
}

// Params 是 argon2id 的成本参数。
type Params struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams 返回项目约定的生产成本：64MiB 内存、3 次迭代、4 路并行。
func DefaultParams() Params {
	return Params{
		Memory:      64 * 1024,
		Iterations:  3,
		Parallelism: 4,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// QuickParams 返回专用于单元测试的轻量成本参数（1MiB 内存、1 次迭代），规避单测耗时过长。
func QuickParams() Params {
	return Params{
		Memory:      1024,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// Hasher 负责基于 Argon2id 的口令加密与校验，实现 PasswordHasher 接口。
type Hasher struct {
	params Params
}

var _ PasswordHasher = (*Hasher)(nil)

// New 构造使用默认生产成本的 Hasher。
func New() *Hasher {
	return &Hasher{params: DefaultParams()}
}

// NewWithParams 依据自定义成本参数构造 Hasher。
func NewWithParams(params Params) *Hasher {
	return &Hasher{params: params}
}

// Make 生成形如 $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash> 的编码串。
func (h *Hasher) Make(password string) (string, error) {
	if len(password) > MaxPasswordLength {
		return "", ErrPasswordTooLong
	}

	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("hasher: generate salt failed: %w", err)
	}

	key := argon2.IDKey(
		[]byte(password),
		salt,
		h.params.Iterations,
		h.params.Memory,
		h.params.Parallelism,
		h.params.KeyLength,
	)

	encoding := base64.RawStdEncoding
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.params.Memory,
		h.params.Iterations,
		h.params.Parallelism,
		encoding.EncodeToString(salt),
		encoding.EncodeToString(key),
	), nil
}

// Check 校验口令是否与编码串匹配；编码串损坏时返回错误而非 false。
func (h *Hasher) Check(encoded, password string) (bool, error) {
	params, salt, expected, err := decode(encoded)
	if err != nil {
		return false, err
	}

	keyLength, err := checkedLength(len(expected))
	if err != nil {
		return false, err
	}

	actual := argon2.IDKey(
		[]byte(password),
		salt,
		params.Iterations,
		params.Memory,
		params.Parallelism,
		keyLength,
	)
	// 常量时间比较，避免通过响应耗时侧信道推断口令。
	return subtle.ConstantTimeCompare(expected, actual) == 1, nil
}

// checkedLength 安全转换字节切片长度为 uint32，规避整数溢出风险。
func checkedLength(value int) (uint32, error) {
	if value < 0 || int64(value) > math.MaxUint32 {
		return 0, ErrInvalidEncodedHash
	}
	return uint32(value), nil
}

// decode 拆解 PHC 编码串，返回其中的成本参数、盐值与摘要。
func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrInvalidEncodedHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return Params{}, nil, nil, ErrInvalidEncodedHash
	}
	if version != argon2.Version {
		return Params{}, nil, nil, ErrIncompatibleVersion
	}

	var params Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.Memory, &params.Iterations, &params.Parallelism); err != nil {
		return Params{}, nil, nil, ErrInvalidEncodedHash
	}

	encoding := base64.RawStdEncoding
	salt, err := encoding.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, ErrInvalidEncodedHash
	}
	key, err := encoding.DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, ErrInvalidEncodedHash
	}

	saltLength, err := checkedLength(len(salt))
	if err != nil {
		return Params{}, nil, nil, err
	}
	keyLength, err := checkedLength(len(key))
	if err != nil {
		return Params{}, nil, nil, err
	}

	params.SaltLength = saltLength
	params.KeyLength = keyLength
	return params, salt, key, nil
}

// NeedsRehash 检查编码串是否需要重新哈希（算法不匹配、解码失败或成本参数落后）。
func (h *Hasher) NeedsRehash(encoded string) bool {
	params, _, _, err := decode(encoded)
	if err != nil {
		return true
	}
	return params.Memory != h.params.Memory ||
		params.Iterations != h.params.Iterations ||
		params.Parallelism != h.params.Parallelism ||
		params.KeyLength != h.params.KeyLength
}

// BcryptHasher 基于标准 Bcrypt 实现口令哈希（用于兼容存量系统迁移）。
type BcryptHasher struct {
	cost int
}

var _ PasswordHasher = (*BcryptHasher)(nil)

// NewBcrypt 构造指定成本的 BcryptHasher，默认使用 bcrypt.DefaultCost (10)。
func NewBcrypt(cost ...int) *BcryptHasher {
	c := cmp.Or(lo.FirstOrEmpty(cost), bcrypt.DefaultCost)
	if c < bcrypt.MinCost || c > bcrypt.MaxCost {
		c = bcrypt.DefaultCost
	}
	return &BcryptHasher{cost: c}
}

// Make 使用 Bcrypt 算法生成口令哈希。
func (b *BcryptHasher) Make(password string) (string, error) {
	if len(password) > MaxPasswordLength {
		return "", ErrPasswordTooLong
	}
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), b.cost)
	if err != nil {
		return "", fmt.Errorf("hasher: bcrypt generate failed: %w", err)
	}
	return string(bytes), nil
}

// Check 校验明文口令是否匹配 Bcrypt 编码串。
func (b *BcryptHasher) Check(encoded, password string) (bool, error) {
	err := bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return false, nil
	}
	return false, err
}

// NeedsRehash 检查 Bcrypt 哈希的成本是否低于期望配置。
func (b *BcryptHasher) NeedsRehash(encoded string) bool {
	cost, err := bcrypt.Cost([]byte(encoded))
	if err != nil {
		return true
	}
	return cost < b.cost
}

// Manager 聚合主哈希器与多算法兼容校验，在用户登录时支持无感升级重算。
type Manager struct {
	primary PasswordHasher
	bcrypt  *BcryptHasher
}

// NewManager 构建口令管理器，默认以指定 hasher 为主哈希驱动，并内置 Bcrypt 兼容能力。
func NewManager(primary PasswordHasher) *Manager {
	return &Manager{
		primary: primary,
		bcrypt:  NewBcrypt(),
	}
}

// Make 使用主哈希器计算口令哈希。
func (m *Manager) Make(password string) (string, error) {
	return m.primary.Make(password)
}

// isBcryptHash 识别编码串是否为 Bcrypt 算法格式 ($2a$, $2b$, $2y$)。
func isBcryptHash(encoded string) bool {
	return strings.HasPrefix(encoded, "$2a$") || strings.HasPrefix(encoded, "$2b$") || strings.HasPrefix(encoded, "$2y$")
}

// Check 自动识别哈希算法版本（兼容 Bcrypt 与 Argon2id）并执行校验。
func (m *Manager) Check(encoded, password string) (bool, error) {
	if isBcryptHash(encoded) {
		return m.bcrypt.Check(encoded, password)
	}
	return m.primary.Check(encoded, password)
}

// NeedsRehash 检测口令哈希是否需要升级为当前主哈希器的最新参数。
func (m *Manager) NeedsRehash(encoded string) bool {
	if isBcryptHash(encoded) {
		return true
	}
	return m.primary.NeedsRehash(encoded)
}
