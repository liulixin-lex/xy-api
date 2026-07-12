package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/casbin/casbin/v2"
	"gorm.io/gorm"
)

// Can reports whether the subject may perform the permission. A superuser role
// short-circuits to allow. Otherwise a per-user override wins, then the union of
// the subject's role baselines applies.
func Can(userID int, systemRole int, permission Permission) bool {
	roles := resolveSubjectRoles(userID, systemRole)
	if len(roles) == 0 {
		return false
	}
	for _, role := range roles {
		if isSuperuserRole(role) {
			return true
		}
	}
	if !isKnownPermission(permission) {
		return false
	}

	e := currentEnforcer()
	if e == nil {
		return false
	}
	if effect, ok := explicitSubjectEffect(e, UserSubject(userID), permission); ok {
		return effect == EffectAllow
	}
	for _, role := range roles {
		if roleBaselineAllows(e, role, permission) {
			return true
		}
	}
	return false
}

// RequiresFreshPolicy identifies high-risk permissions whose revocation must
// take effect immediately across all application nodes.
func RequiresFreshPolicy(permission Permission) bool {
	if permission.Resource != ResourceChannelRouting {
		return false
	}
	switch permission.Action {
	case ActionDeploy, ActionAuditExport, ActionSensitiveWrite:
		return true
	default:
		return false
	}
}

// CanCurrent evaluates high-risk permissions against the policy database
// instead of the node-local Casbin snapshot. The database user status and role
// are authoritative, including for root users; non-root subjects preserve the
// normal user-override-first authorization semantics.
func CanCurrent(ctx context.Context, userID int, systemRole int, permission Permission) (bool, error) {
	if !isKnownPermission(permission) {
		return false, nil
	}
	if !RequiresFreshPolicy(permission) {
		return Can(userID, systemRole, permission), nil
	}

	db := currentPolicyDB()
	if db == nil {
		return false, fmt.Errorf("authz policy database is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var currentUser model.User
	if err := db.WithContext(ctx).
		Select("id", "role", "status").
		Where("id = ?", userID).
		First(&currentUser).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if currentUser.Status != common.UserStatusEnabled || currentUser.Role < common.RoleAdminUser {
		return false, nil
	}
	roles := resolveSubjectRoles(userID, currentUser.Role)
	if len(roles) == 0 {
		return false, nil
	}
	for _, role := range roles {
		if isSuperuserRole(role) {
			return true, nil
		}
	}
	if effect, found, err := currentSubjectEffect(ctx, db, UserSubject(userID), permission); err != nil {
		return false, err
	} else if found {
		return effect == EffectAllow, nil
	}
	for _, role := range roles {
		effect, found, err := currentSubjectEffect(ctx, db, RoleSubject(role), permission)
		if err != nil {
			return false, err
		}
		if found && effect == EffectAllow {
			return true, nil
		}
	}
	return false, nil
}

func currentSubjectEffect(
	ctx context.Context,
	db *gorm.DB,
	subject string,
	permission Permission,
) (string, bool, error) {
	var rules []model.CasbinRule
	if err := db.WithContext(ctx).
		Where(
			"ptype = ? AND v0 = ? AND v1 = ? AND v2 = ?",
			"p", subject, permission.Resource, permission.Action,
		).
		Order("id asc").Find(&rules).Error; err != nil {
		return "", false, err
	}
	hasAllow := false
	for _, rule := range rules {
		switch rule.V3 {
		case EffectDeny:
			return EffectDeny, true, nil
		case "", EffectAllow:
			hasAllow = true
		}
	}
	if hasAllow {
		return EffectAllow, true, nil
	}
	return "", false, nil
}

// Capabilities returns the full resource/action matrix the subject is allowed.
func Capabilities(userID int, systemRole int) PermissionsMap {
	result := make(PermissionsMap, len(registry))
	for _, resource := range registry {
		actions := make(map[string]bool, len(resource.Actions))
		for _, action := range resource.Actions {
			actions[action.Action] = Can(userID, systemRole, Permission{
				Resource: resource.Resource,
				Action:   action.Action,
			})
		}
		result[resource.Resource] = actions
	}
	return result
}

func roleBaselineAllows(e *casbin.SyncedEnforcer, roleKey string, permission Permission) bool {
	effect, ok := explicitSubjectEffect(e, RoleSubject(roleKey), permission)
	return ok && effect == EffectAllow
}

func explicitSubjectEffect(e *casbin.SyncedEnforcer, subject string, permission Permission) (string, bool) {
	policies, err := e.GetFilteredPolicy(0, subject, permission.Resource, permission.Action)
	if err != nil {
		return "", false
	}
	hasAllow := false
	for _, policy := range policies {
		switch policyEffect(policy) {
		case EffectDeny:
			return EffectDeny, true
		case EffectAllow:
			hasAllow = true
		}
	}
	if hasAllow {
		return EffectAllow, true
	}
	return "", false
}

func policyEffect(policy []string) string {
	if len(policy) < 4 || policy[3] == "" {
		return EffectAllow
	}
	return policy[3]
}
