package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMySQLMigrationLockNameIsSchemaScopedAndBounded(t *testing.T) {
	first := mysqlMigrationLockName("new_api_primary")
	assert.Equal(t, first, mysqlMigrationLockName("new_api_primary"))
	assert.NotEqual(t, first, mysqlMigrationLockName("new_api_secondary"))
	assert.LessOrEqual(t, len(first), 64)
	assert.LessOrEqual(t, len(mysqlMigrationLockName(strings.Repeat("schema", 64))), 64)
}
