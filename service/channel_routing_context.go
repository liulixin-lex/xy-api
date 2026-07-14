package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
)

type SelectedRoutingIdentity struct {
	ChannelID         int
	SnapshotRevision  uint64
	PoolID            int
	MemberID          int
	CredentialID      int
	UpstreamAccountID int
}

func SetSelectedRoutingIdentity(c *gin.Context, identity SelectedRoutingIdentity) {
	if c == nil {
		return
	}
	if identity.ChannelID <= 0 || identity.SnapshotRevision == 0 || identity.PoolID <= 0 || identity.MemberID <= 0 {
		ClearSelectedRoutingIdentity(c)
		return
	}
	common.SetContextKey(c, constant.ContextKeyRoutingSelectedIdentity, identity)
}

func GetSelectedRoutingIdentity(c *gin.Context, channelID int) (SelectedRoutingIdentity, bool) {
	if c == nil || channelID <= 0 {
		return SelectedRoutingIdentity{}, false
	}
	identity, ok := common.GetContextKeyType[SelectedRoutingIdentity](c, constant.ContextKeyRoutingSelectedIdentity)
	if !ok || identity.ChannelID != channelID || identity.SnapshotRevision == 0 || identity.PoolID <= 0 || identity.MemberID <= 0 {
		return SelectedRoutingIdentity{}, false
	}
	return identity, true
}

func ClearSelectedRoutingIdentity(c *gin.Context) {
	if c == nil {
		return
	}
	common.SetContextKey(c, constant.ContextKeyRoutingSelectedIdentity, nil)
}
