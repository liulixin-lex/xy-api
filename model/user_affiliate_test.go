package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetInvitedUsersReturnsOnlySafeUsersForInviter(t *testing.T) {
	truncateTables(t)

	users := []User{
		{Id: 1, Username: "inviter", Password: "secret", Status: common.UserStatusEnabled, AffCode: "aff1"},
		{Id: 2, Username: "other-inviter", Password: "secret", Status: common.UserStatusEnabled, AffCode: "aff2"},
		{Id: 3, Username: "alice", DisplayName: "Alice", Password: "secret", AffCode: "aff3", InviterId: 1, CreatedAt: 300},
		{Id: 4, Username: "bob", DisplayName: "Bob", Password: "secret", AffCode: "aff4", InviterId: 1, CreatedAt: 400},
		{Id: 5, Username: "mallory", DisplayName: "Mallory", Password: "secret", AffCode: "aff5", InviterId: 2, CreatedAt: 500},
	}
	require.NoError(t, DB.Create(&users).Error)

	invited, err := GetInvitedUsers(1)

	require.NoError(t, err)
	require.Len(t, invited, 2)
	assert.Equal(t, 4, invited[0].Id)
	assert.Equal(t, "bob", invited[0].Username)
	assert.Equal(t, "Bob", invited[0].DisplayName)
	assert.Equal(t, int64(400), invited[0].CreatedAt)
	assert.Equal(t, 3, invited[1].Id)
	assert.Equal(t, "alice", invited[1].Username)
}
