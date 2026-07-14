package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func billingUniqueReceiptIndexReady(
	db *gorm.DB,
	databaseType common.DatabaseType,
	tableName string,
	indexName string,
	columnName string,
) (bool, error) {
	if db == nil {
		return false, errors.New("billing receipt database is unavailable")
	}
	for _, identifier := range []string{tableName, indexName, columnName} {
		if identifier == "" {
			return false, errors.New("billing receipt schema identifier is empty")
		}
		for _, character := range identifier {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '_' {
				return false, errors.New("billing receipt schema identifier is invalid")
			}
		}
	}

	var columns []struct {
		Name         string `gorm:"column:name"`
		PrefixLength *int   `gorm:"column:prefix_length"`
	}
	switch databaseType {
	case common.DatabaseTypeSQLite:
		var validIndexCount int64
		indexQuery := fmt.Sprintf(`SELECT count(*) FROM pragma_index_list('%s')
WHERE name = ? AND "unique" = 1 AND partial = 0`, tableName)
		if err := db.Raw(indexQuery, indexName).Scan(&validIndexCount).Error; err != nil {
			return false, err
		}
		if validIndexCount != 1 {
			return false, nil
		}
		query := fmt.Sprintf("SELECT name FROM pragma_index_info('%s') ORDER BY seqno", indexName)
		if err := db.Raw(query).Scan(&columns).Error; err != nil {
			return false, err
		}
	case common.DatabaseTypeMySQL:
		if err := db.Raw(`SELECT column_name AS name, sub_part AS prefix_length FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ? AND non_unique = 0
ORDER BY seq_in_index`, tableName, indexName).Scan(&columns).Error; err != nil {
			return false, err
		}
	case common.DatabaseTypePostgreSQL:
		if err := db.Raw(`SELECT attribute.attname AS name
FROM pg_catalog.pg_class AS table_class
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = table_class.relnamespace
JOIN pg_catalog.pg_index AS index_meta ON index_meta.indrelid = table_class.oid
JOIN pg_catalog.pg_class AS index_class ON index_class.oid = index_meta.indexrelid
JOIN pg_catalog.pg_attribute AS attribute ON attribute.attrelid = table_class.oid
	AND attribute.attnum = ANY(index_meta.indkey::smallint[])
WHERE namespace.nspname = current_schema() AND table_class.relname = ?
	AND index_class.relname = ? AND index_meta.indisunique = TRUE
	AND index_meta.indisvalid = TRUE AND index_meta.indisready = TRUE
	AND index_meta.indpred IS NULL AND index_meta.indexprs IS NULL AND index_meta.indnatts = 1
ORDER BY array_position(index_meta.indkey::smallint[], attribute.attnum)`, tableName, indexName).
			Scan(&columns).Error; err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("unsupported billing receipt database type %q", databaseType)
	}
	return len(columns) == 1 && columns[0].Name == columnName && columns[0].PrefixLength == nil, nil
}
