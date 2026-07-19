package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type tokenAuthenticationRow struct {
	Token
	AuthUserID            int    `gorm:"column:auth_user_id"`
	AuthUsername          string `gorm:"column:auth_username"`
	AuthUserRole          int    `gorm:"column:auth_user_role"`
	AuthUserStatus        int    `gorm:"column:auth_user_status"`
	AuthUserPaymentFrozen bool   `gorm:"column:auth_user_payment_frozen"`
	AuthUserGroup         string `gorm:"column:auth_user_group"`
	AuthUserEmail         string `gorm:"column:auth_user_email"`
	AuthUserQuota         int    `gorm:"column:auth_user_quota"`
	AuthUserSetting       string `gorm:"column:auth_user_setting"`
}

// GetTokenAndUserForAuthentication loads the token and its current user in one
// primary-database query. Redis remains an optimization for non-authentication
// reads, but cached token/user snapshots are never trusted for access control.
func GetTokenAndUserForAuthentication(key string) (*Token, *User, error) {
	if strings.TrimSpace(key) == "" {
		return nil, nil, ErrTokenNotProvided
	}
	if DB == nil {
		return nil, nil, fmt.Errorf("%w: database unavailable", ErrDatabase)
	}

	statement := &gorm.Statement{DB: DB}
	tokenKeyColumn := statement.Quote(clause.Column{Table: "tokens", Name: "key"})
	userGroupColumn := statement.Quote(clause.Column{Table: "users", Name: "group"})
	selectColumns := strings.Join([]string{
		"tokens.*",
		"users.id AS auth_user_id",
		"users.username AS auth_username",
		"users.role AS auth_user_role",
		"users.status AS auth_user_status",
		"users.payment_frozen AS auth_user_payment_frozen",
		userGroupColumn + " AS auth_user_group",
		"users.email AS auth_user_email",
		"users.quota AS auth_user_quota",
		"users.setting AS auth_user_setting",
	}, ", ")

	var row tokenAuthenticationRow
	err := DB.Model(&Token{}).
		Select(selectColumns).
		Joins("JOIN users ON users.id = tokens.user_id AND users.deleted_at IS NULL").
		Where(tokenKeyColumn+" = ?", key).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, gorm.ErrRecordNotFound
		}
		return nil, nil, fmt.Errorf("%w: %v", ErrDatabase, err)
	}

	user := &User{
		Id:            row.AuthUserID,
		Username:      row.AuthUsername,
		Role:          row.AuthUserRole,
		Status:        row.AuthUserStatus,
		PaymentFrozen: row.AuthUserPaymentFrozen,
		Group:         row.AuthUserGroup,
		Email:         row.AuthUserEmail,
		Quota:         row.AuthUserQuota,
		Setting:       row.AuthUserSetting,
	}
	return &row.Token, user, nil
}

// ValidateUserTokenAndUser is the authoritative relay-token authentication
// lookup. It intentionally performs no Redis read, so a failed cache
// invalidation can only cause a cache miss elsewhere, never continued access.
func ValidateUserTokenAndUser(key string) (*Token, *User, error) {
	token, user, err := GetTokenAndUserForAuthentication(key)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrTokenNotProvided) {
			return nil, nil, ErrTokenInvalid
		}
		return nil, nil, err
	}

	if _, invalid := tokenAuthenticationFailureStatus(token); invalid {
		return token, user, ErrTokenInvalid
	}
	return token, user, nil
}

func tokenAuthenticationFailureStatus(token *Token) (int, bool) {
	if token == nil {
		return common.TokenStatusDisabled, true
	}
	if token.Status != common.TokenStatusEnabled {
		return token.Status, true
	}
	if token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp() {
		return common.TokenStatusExpired, true
	}
	if !token.UnlimitedQuota && token.RemainQuota <= 0 {
		return common.TokenStatusExhausted, true
	}
	return common.TokenStatusEnabled, false
}
