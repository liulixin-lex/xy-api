package model

import (
	"context"
	"strconv"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// routingPolicyDeployAuthorizedActorsDBContext returns the approval actors
// that are still enabled and currently authorized to deploy channel routing
// policy. The caller's transaction locks the relevant identity and grant rows
// so a concurrent revocation cannot race a publish or rollback commit.
func routingPolicyDeployAuthorizedActorsDBContext(
	ctx context.Context,
	db *gorm.DB,
	actorIDs []int,
) (map[int]struct{}, error) {
	authorized := make(map[int]struct{}, len(actorIDs))
	if db == nil {
		return authorized, ErrRoutingPolicyApprovalInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	uniqueActorIDs := make([]int, 0, len(actorIDs))
	seenActorIDs := make(map[int]struct{}, len(actorIDs))
	for _, actorID := range actorIDs {
		if actorID <= 0 {
			continue
		}
		if _, exists := seenActorIDs[actorID]; exists {
			continue
		}
		seenActorIDs[actorID] = struct{}{}
		uniqueActorIDs = append(uniqueActorIDs, actorID)
	}
	if len(uniqueActorIDs) == 0 {
		return authorized, nil
	}

	var users []User
	if err := lockForUpdate(db.WithContext(ctx)).
		Model(&User{}).
		Select("id", "role", "status").
		Where("id IN ?", uniqueActorIDs).
		Find(&users).Error; err != nil {
		return nil, err
	}
	adminSubjects := make([]string, 0, len(users))
	actorBySubject := make(map[string]int, len(users))
	for _, user := range users {
		if user.Status != common.UserStatusEnabled {
			continue
		}
		if user.Role >= common.RoleRootUser {
			authorized[user.Id] = struct{}{}
			continue
		}
		if user.Role < common.RoleAdminUser {
			continue
		}
		subject := "user:" + strconv.Itoa(user.Id)
		adminSubjects = append(adminSubjects, subject)
		actorBySubject[subject] = user.Id
	}
	if len(adminSubjects) == 0 {
		return authorized, nil
	}

	policySubjects := append(append(make([]string, 0, len(adminSubjects)+1), adminSubjects...), "role:admin")
	var rules []CasbinRule
	if err := lockForUpdate(db.WithContext(ctx)).
		Where(
			"ptype = ? AND v0 IN ? AND v1 = ? AND v2 = ?",
			"p", policySubjects, "channel_routing", "deploy",
		).
		Order("id asc").Find(&rules).Error; err != nil {
		return nil, err
	}
	allowedSubjects := make(map[string]struct{}, len(rules))
	deniedSubjects := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		switch rule.V3 {
		case "deny":
			deniedSubjects[rule.V0] = struct{}{}
		case "", "allow":
			allowedSubjects[rule.V0] = struct{}{}
		}
	}
	_, roleDenied := deniedSubjects["role:admin"]
	_, roleAllowed := allowedSubjects["role:admin"]
	roleBaselineAllows := roleAllowed && !roleDenied
	for subject, actorID := range actorBySubject {
		if _, denied := deniedSubjects[subject]; denied {
			continue
		}
		if _, allowed := allowedSubjects[subject]; allowed {
			authorized[actorID] = struct{}{}
			continue
		}
		if roleBaselineAllows {
			authorized[actorID] = struct{}{}
		}
	}
	return authorized, nil
}
