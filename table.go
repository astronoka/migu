package migu

import (
	"database/sql"
	"fmt"
)

// Table is table definitions
type Table struct {
	Name    string
	Columns []*columnSchema
	Indexes []*Index
}

func newTablesFromDB(db *sql.DB) (map[string]*Table, error) {
	dbname, err := getCurrentDBName(db)
	if err != nil {
		return nil, err
	}
	tableColumns, err := getTableColumns(db, dbname)
	if err != nil {
		return nil, fmt.Errorf("migu: get table map failed. " + err.Error())
	}
	tables := make(map[string]*Table)
	for tableName, columns := range tableColumns {
		tables[tableName] = &Table{
			Name:    tableName,
			Columns: columns,
		}
	}

	indexMap, err := getIndexMap(db, dbname)
	if err != nil {
		return nil, err
	}
	for tableName, table := range tables {
		indexes, exist := indexMap[tableName]
		if !exist {
			continue
		}
		for _, index := range indexes {
			table.Indexes = append(table.Indexes, index)
		}
	}

	return tables, nil
}

func getCurrentDBName(db *sql.DB) (string, error) {
	var dbname sql.NullString
	err := db.QueryRow(`SELECT DATABASE()`).Scan(&dbname)
	return dbname.String, err
}

func getTableColumns(db *sql.DB, dbname string) (map[string][]*columnSchema, error) {
	query := `
SELECT
  TABLE_NAME,
  COLUMN_NAME,
  COLUMN_DEFAULT,
  IS_NULLABLE,
  DATA_TYPE,
  CHARACTER_MAXIMUM_LENGTH,
  CHARACTER_OCTET_LENGTH,
  NUMERIC_PRECISION,
  NUMERIC_SCALE,
  COLUMN_TYPE,
  COLUMN_KEY,
  EXTRA,
  COLUMN_COMMENT
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = ?
ORDER BY TABLE_NAME, ORDINAL_POSITION`
	rows, err := db.Query(query, dbname)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tableColumns := map[string][]*columnSchema{}
	for rows.Next() {
		schema := &columnSchema{}
		if err := rows.Scan(
			&schema.TableName,
			&schema.ColumnName,
			&schema.ColumnDefault,
			&schema.IsNullable,
			&schema.DataType,
			&schema.CharacterMaximumLength,
			&schema.CharacterOctetLength,
			&schema.NumericPrecision,
			&schema.NumericScale,
			&schema.ColumnType,
			&schema.ColumnKey,
			&schema.Extra,
			&schema.ColumnComment,
		); err != nil {
			return nil, err
		}
		tableColumns[schema.TableName] = append(tableColumns[schema.TableName], schema)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tableColumns, nil
}

func getIndexMap(db *sql.DB, dbname string) (map[string]map[string]*Index, error) {
	query := `
SELECT
  TABLE_NAME,
  NON_UNIQUE,
  INDEX_NAME,
  SEQ_IN_INDEX,
  COLUMN_NAME,
  COLLATION
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = ?
ORDER BY
  TABLE_NAME,
  INDEX_NAME,
  SEQ_IN_INDEX
`
	rows, err := db.Query(query, dbname)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// map[TABLE_NAME][INDEX_NAME]index
	// https://dev.mysql.com/doc/refman/5.6/en/show-index.html
	indexMap := make(map[string]map[string]*Index)
	for rows.Next() {
		var (
			tableName  string
			nonUnique  int64
			indexName  string
			seqInIndex int64
			columnName string
			collation  *string
		)
		if err := rows.Scan(&tableName, &nonUnique, &indexName, &seqInIndex, &columnName, &collation); err != nil {
			return nil, err
		}
		if _, exists := indexMap[tableName]; !exists {
			indexMap[tableName] = make(map[string]*Index)
		}
		if _, exists := indexMap[tableName][indexName]; !exists {
			indexMap[tableName][indexName] = &Index{
				Unique:      nonUnique == 0,
				Name:        indexName,
				ColumnNames: []string{},
			}
		}
		index := indexMap[tableName][indexName]
		index.ColumnNames = append(index.ColumnNames, columnName)
	}
	return indexMap, rows.Err()
}

func (t Table) HasDatetimeColumn() bool {
	for _, column := range t.Columns {
		if column.DataType == "datetime" {
			return true
		}
	}
	return false
}

func (t Table) ColumnMap() map[string]*columnSchema {
	m := map[string]*columnSchema{}
	for _, column := range t.Columns {
		m[column.ColumnName] = column
	}
	return m
}

func (t Table) IndexMap() map[string]*Index {
	m := map[string]*Index{}
	for _, index := range t.Indexes {
		m[index.Name] = index
	}
	return m
}
