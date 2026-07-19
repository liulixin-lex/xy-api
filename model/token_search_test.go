package model

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchUserTokensEscapesWildcardsAndCapsResultPages(t *testing.T) {
	truncateTables(t)

	const literalUserID = 993101
	literalTokens := []*Token{
		{UserId: literalUserID, Name: "alpha_one", Key: "literal-underscore-token"},
		{UserId: literalUserID, Name: "alphaXone", Key: "literal-letter-token"},
	}
	require.NoError(t, DB.Create(literalTokens).Error)

	matches, total, err := SearchUserTokens(literalUserID, "alpha_one", "", 0, searchHardLimit)
	require.NoError(t, err)
	require.Len(t, matches, 1)
	assert.Equal(t, "alpha_one", matches[0].Name)
	assert.EqualValues(t, 1, total)

	_, _, err = SearchUserTokens(literalUserID, "%", "", 0, searchHardLimit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "关键词长度")

	const pageUserID = 993102
	pageTokens := make([]*Token, 0, searchHardLimit+1)
	for index := 0; index < searchHardLimit+1; index++ {
		pageTokens = append(pageTokens, &Token{
			UserId: pageUserID,
			Name:   fmt.Sprintf("page-token-%03d", index),
			Key:    fmt.Sprintf("page-token-key-%03d", index),
		})
	}
	require.NoError(t, DB.CreateInBatches(pageTokens, 50).Error)

	matches, total, err = SearchUserTokens(pageUserID, "", "", 0, searchHardLimit+50)
	require.NoError(t, err)
	assert.Len(t, matches, searchHardLimit)
	assert.EqualValues(t, searchHardLimit+1, total)
}
