package user

import (
	"context"
	"errors"
	"strings"

	"cgin/internal/database/sqlc"
	"cgin/internal/enum"
	"cgin/pkg/errs"
	"cgin/pkg/validator"
)

// RegisterParams 描述创建一个带本地口令凭据的用户所需的输入。
type RegisterParams struct {
	Name          string
	Email         string
	Username      *string
	Image         *string
	Role          enum.Role
	Status        enum.Status
	EmailVerified bool
	Password      string
}

// Register 在事务中原子创建用户与本地口令凭据，并统一归一化与默认值规则。
func (s *Service) Register(ctx context.Context, params RegisterParams) (*sqlc.User, error) {
	if err := validator.PasswordStrength("password", params.Password); err != nil {
		return nil, err
	}
	if s.hasher == nil {
		return nil, errs.Internal(errors.New("user: service hasher is not configured"))
	}

	passwordHash, err := s.hasher.Make(params.Password)
	if err != nil {
		return nil, errs.Internal(err)
	}

	createParams := sqlc.CreateUserParams{
		Name:          strings.TrimSpace(params.Name),
		Email:         NormaliseEmail(params.Email),
		EmailVerified: params.EmailVerified,
		Image:         params.Image,
		Role:          string(defaultRole(params.Role)),
		Status:        string(defaultStatus(params.Status)),
	}
	if params.Username != nil {
		username := NormaliseUsername(*params.Username)
		createParams.Username = &username
		createParams.DisplayUsername = &username
	}

	var created sqlc.User
	err = s.db.RunInTx(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		row, err := q.CreateUser(ctx, createParams)
		if err != nil {
			return err
		}
		if _, err := q.CreateAccount(ctx, sqlc.CreateAccountParams{
			UserID:     row.ID,
			AccountID:  row.ID,
			ProviderID: string(enum.ProviderCredential),
			Password:   &passwordHash,
		}); err != nil {
			return err
		}
		created = row
		return nil
	})
	if err != nil {
		return nil, MapUniqueConflict(err)
	}
	return &created, nil
}

// defaultRole 在未显式指定角色时回落为普通用户。
func defaultRole(role enum.Role) enum.Role {
	if role == "" {
		return enum.RoleUser
	}
	return role
}

// defaultStatus 在未显式指定状态时回落为正常活跃。
func defaultStatus(status enum.Status) enum.Status {
	if status == "" {
		return enum.StatusActive
	}
	return status
}
