package migu

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"

	"github.com/astronoka/migu/dialect"
)

type TableAST struct {
	Name      string
	ColumnAST *ast.StructType
	IndexAST  *ast.StructType
}

func newTableASTsFromFile(filename string, src interface{}) (map[string]*TableAST, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	ast.FileExports(f)
	tableASTMap := map[string]*TableAST{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.TypeSpec:
			if t, ok := x.Type.(*ast.StructType); ok {
				tableName := x.Name.Name
				isIndex := false
				if strings.HasSuffix(x.Name.Name, "Index") {
					tableName = strings.TrimSuffix(x.Name.Name, "Index")
					isIndex = true
				}
				schemaTableName := toSchemaTableName(tableName)
				if _, exist := tableASTMap[schemaTableName]; !exist {
					tableASTMap[schemaTableName] = &TableAST{Name: tableName}
				}
				if isIndex {
					tableASTMap[schemaTableName].IndexAST = t
				} else {
					tableASTMap[schemaTableName].ColumnAST = t
				}
			}
			return false
		default:
			return true
		}
	})
	return tableASTMap, nil
}

func (t *TableAST) HasSchema() bool {
	return t.ColumnAST != nil
}

func (t *TableAST) ColumnMap() map[string]*Column {
	m := map[string]*Column{}
	for _, column := range t.MustColumns() {
		m[column.Name] = column
	}
	return m
}

func (t *TableAST) IndexMap() map[string]*Index {
	m := map[string]*Index{}
	for _, index := range t.MustIndexes() {
		m[index.Name] = index
	}
	return m
}

func (t *TableAST) Columns() ([]*Column, error) {
	models := make([]*Column, 0)
	if !t.HasSchema() {
		return models, fmt.Errorf("migu: TableAST.Columns error: %s schema is empty", t.Name)
	}

	for _, fld := range t.ColumnAST.Fields.List {
		typeName, err := detectTypeName(fld)
		if err != nil {
			return nil, fmt.Errorf("migu: TableAST.Columns error: " + err.Error())
		}
		f, err := newColumnFromAST(typeName, fld)
		if err != nil {
			return nil, fmt.Errorf("migu: TableAST.Columns error: " + err.Error())
		}
		if f.Ignore {
			continue
		}
		for _, ident := range fld.Names {
			field := *f
			field.Name = toSchemaFieldName(ident.Name)
			models = append(models, &field)
		}
	}
	return models, nil
}

func (t *TableAST) Indexes() ([]*Index, error) {
	indexes := make([]*Index, 0)
	if t.IndexAST == nil {
		return indexes, nil
	}
	for _, fld := range t.IndexAST.Fields.List {
		if fld.Tag == nil {
			continue
		}
		s, err := strconv.Unquote(fld.Tag.Value)
		if err != nil {
			return nil, fmt.Errorf("migu: TableAST.Indexes error. " + err.Error())
		}
		index, err := parseIndexStructTag(reflect.StructTag(s))
		if err != nil {
			return nil, fmt.Errorf("migu: TableAST.Indexes error. " + err.Error())
		}
		indexes = append(indexes, index)
	}
	return indexes, nil
}

func (t *TableAST) MustColumns() []*Column {
	c, err := t.Columns()
	if err != nil {
		panic(err)
	}
	return c
}

func (t *TableAST) MustIndexes() []*Index {
	i, err := t.Indexes()
	if err != nil {
		panic(err)
	}
	return i
}

// see: https://dev.mysql.com/doc/refman/5.6/en/create-table.html
func (t *TableAST) CreateTableQuery(d dialect.Dialect) ([]string, error) {
	model, err := t.Columns()
	if err != nil {
		return nil, fmt.Errorf("migu: TableAST.CreateTableQuery error. " + err.Error())
	}
	columns := make([]string, len(model))
	for i, f := range model {
		columns[i] = columnSQL(d, f)
	}

	indexes, err := t.indexDefinitions(d)
	if err != nil {
		return nil, fmt.Errorf("migu: TableAST.CreateTableQuery error. " + err.Error())
	}

	createDefinitions := append(columns, indexes...)
	createTableQuery := fmt.Sprintf(`CREATE TABLE %s (
  %s
)`, d.Quote(toSchemaTableName(t.Name)), strings.Join(createDefinitions, ", "))

	return []string{createTableQuery}, nil
}

func (t *TableAST) indexDefinitions(d dialect.Dialect) ([]string, error) {
	indexes, err := t.Indexes()
	if err != nil {
		return nil, fmt.Errorf("migu: TableAST.indexDefinitions error. " + err.Error())
	}

	indexDefinitions := make([]string, 0)
	for _, index := range indexes {
		indexDefinitions = append(indexDefinitions, index.AsCreateTableDefinition(d))
	}
	return indexDefinitions, nil
}

func (t *TableAST) AlterTableQueries(d dialect.Dialect, currentTable *Table) ([]string, error) {
	tableName := toSchemaTableName(t.Name)
	currentColumMap := currentTable.ColumnMap()
	currentIndexMap := currentTable.IndexMap()
	addSQLs, err := t.GenerateAddFieldSQLs(d, currentColumMap)
	if err != nil {
		return nil, fmt.Errorf("migu: TableAST.AlterTableQueries error: " + err.Error())
	}
	dropSQLs, err := t.GenerateDropFieldSQLs(d, currentColumMap)
	if err != nil {
		return nil, fmt.Errorf("migu: TableAST.AlterTableQueries error: " + err.Error())
	}
	modifySQLs, err := t.GenerateModifyFieldSQLs(d, currentColumMap)
	if err != nil {
		return nil, fmt.Errorf("migu: TableAST.AlterTableQueries error: " + err.Error())
	}

	dropIndexSQLs, err := t.GenerateDropIndexSQLs(d, currentIndexMap)
	if err != nil {
		return nil, fmt.Errorf("migu: TableAST.AlterTableQueries error: " + err.Error())
	}
	addIndexSQLs, err := t.GenerateAddIndexSQLs(d, currentIndexMap)
	if err != nil {
		return nil, fmt.Errorf("migu: TableAST.AlterTableQueries error: " + err.Error())
	}

	migrations := make([]string, 0)
	migrations = append(migrations, addSQLs...)
	migrations = append(migrations, dropSQLs...)
	migrations = append(migrations, modifySQLs...)
	migrations = append(migrations, dropIndexSQLs...)
	migrations = append(migrations, addIndexSQLs...)
	if len(migrations) <= 0 {
		return nil, nil
	}

	alterTableQuery := fmt.Sprintf(`ALTER TABLE %s %s`,
		d.Quote(tableName), strings.Join(migrations, ", "))
	return []string{alterTableQuery}, nil
}

func (t *TableAST) GenerateAddFieldSQLs(d dialect.Dialect, currentColumMap map[string]*columnSchema) ([]string, error) {
	sqls := make([]string, 0)
	expectedColumns := t.MustColumns()
	for i, column := range expectedColumns {
		if _, exist := currentColumMap[column.Name]; exist {
			continue
		}
		position := "FIRST"
		if i > 0 {
			prev := expectedColumns[i-1]
			position = fmt.Sprintf(`AFTER %s`, d.Quote(prev.Name))
		}
		sqls = append(sqls, fmt.Sprintf(`ADD %s %s`, columnSQL(d, column), position))
	}
	return sqls, nil
}

func (t *TableAST) GenerateDropFieldSQLs(d dialect.Dialect, currentColumMap map[string]*columnSchema) ([]string, error) {
	sqls := make([]string, 0)
	newColumnMap := t.ColumnMap()
	for columnName, _ := range currentColumMap {
		if _, exist := newColumnMap[columnName]; exist {
			continue
		}
		sqls = append(sqls, fmt.Sprintf(`DROP %s`, d.Quote(toSchemaTableName(columnName))))
	}
	return sqls, nil
}

func (t *TableAST) GenerateModifyFieldSQLs(d dialect.Dialect, currentColumMap map[string]*columnSchema) ([]string, error) {
	sqls := make([]string, 0)
	for _, column := range t.MustColumns() {
		if _, exist := currentColumMap[column.Name]; !exist {
			continue
		}
		currentColum := currentColumMap[column.Name]
		if !hasDifference(currentColum, column) {
			continue
		}
		sqls = append(sqls, fmt.Sprintf(`MODIFY %s`, columnSQL(d, column)))
	}
	return sqls, nil
}

func (t *TableAST) GenerateAddIndexSQLs(d dialect.Dialect, currentIndexMap map[string]*Index) ([]string, error) {
	sqls := make([]string, 0)
	newIndexMap := t.IndexMap()
	for indexName, index := range newIndexMap {
		if currentIndex, exist := currentIndexMap[indexName]; exist {
			if reflect.DeepEqual(index, currentIndex) {
				continue
			}
		}
		sqls = append(sqls, fmt.Sprintf("ADD %s", index.AsCreateTableDefinition(d)))
	}
	return sqls, nil
}

func (t *TableAST) GenerateDropIndexSQLs(d dialect.Dialect, currentIndexMap map[string]*Index) ([]string, error) {
	sqls := make([]string, 0)
	newIndexMap := t.IndexMap()
	for indexName, index := range currentIndexMap {
		if newIndex, exist := newIndexMap[indexName]; exist {
			if reflect.DeepEqual(index, newIndex) {
				continue
			}
		}

		if index.isPrimaryKey() {
			sqls = append(sqls, "DROP PRIMARY KEY")
		} else {
			sqls = append(sqls, fmt.Sprintf(`DROP INDEX %s`, d.Quote(indexName)))
		}
	}
	return sqls, nil
}

func newColumnFromAST(typeName string, astF *ast.Field) (*Column, error) {
	ret := &Column{
		Type: typeName,
	}
	if astF.Tag != nil {
		s, err := strconv.Unquote(astF.Tag.Value)
		if err != nil {
			return nil, err
		}
		if err := parseStructTag(ret, reflect.StructTag(s)); err != nil {
			return nil, err
		}
	}
	if isSizeRequiredType(ret.Type) {
		if ret.Size == 0 {
			ret.Size = 255
		}
	} else {
		ret.Size = 0
	}
	if astF.Comment != nil {
		ret.Comment = strings.TrimSpace(astF.Comment.Text())
	}
	return ret, nil
}

func parseStructTag(f *Column, tag reflect.StructTag) error {
	migu := tag.Get("migu")
	if migu == "" {
		return nil
	}
	for _, opt := range strings.Split(migu, tagSeparater) {
		optval := strings.SplitN(opt, ":", 2)
		switch optval[0] {
		case tagDefault:
			if len(optval) > 1 {
				if f.Type == "bool" {
					f.Default = normalizeBoolDefaultTagTo0or1(optval[1])
				} else {
					f.Default = optval[1]
				}
			}
		case tagPrimaryKey:
			f.PrimaryKey = true
		case tagAutoIncrement:
			f.AutoIncrement = true
		case tagUnique:
			f.Unique = true
		case tagIgnore:
			f.Ignore = true
		case tagSize:
			if len(optval) < 2 {
				return fmt.Errorf("`size` tag must specify the parameter")
			}
			size, err := strconv.ParseUint(optval[1], 10, 64)
			if err != nil {
				return err
			}
			f.Size = size
		default:
			return fmt.Errorf("unknown option: `%s'", opt)
		}
	}
	return nil
}

func normalizeBoolDefaultTagTo0or1(s string) string {
	switch strings.ToLower(s) {
	case "1", "true", "on":
		return "1"
	}
	return "0"
}

func isSizeRequiredType(typeName string) bool {
	types := []string{"string", "*string", "sql.NullString"}
	return containsString(types, typeName)
}

func parseIndexStructTag(tag reflect.StructTag) (*Index, error) {
	migu := tag.Get("migu")
	if migu == "" {
		return nil, fmt.Errorf("migu: parseIndexStructTag: index tag must not be empty")
	}
	index := &Index{}
	isPrimaryKey := false
	for _, opt := range strings.Split(migu, tagSeparater) {
		optval := strings.SplitN(opt, ":", 2)
		if len(optval) < 1 {
			return nil, fmt.Errorf("migu: parseIndexStructTag: 'migu' tag must specify values")
		}
		switch optval[0] {
		case tagPrimaryKey:
			isPrimaryKey = true
		case tagIndex:
			if len(optval) < 2 {
				return nil, fmt.Errorf("migu: parseIndexStructTag: '%s' tag must specify parameters", tagIndex)
			}
			params := strings.SplitN(optval[1], ",", -1)
			if len(params) < 1 {
				return nil, fmt.Errorf("migu: parseIndexStructTag: '%s' tag must specify one column at least", tagIndex)
			}
			if len(params) == 1 {
				index.Name = params[0]
				index.ColumnNames = params
			} else {
				index.Name = params[0]
				index.ColumnNames = params[1:]
			}
		case tagUnique:
			index.Unique = true
		default:
			return nil, fmt.Errorf("migu: parseIndexStructTag: unknown option: `%s'", opt)
		}
	}
	if isPrimaryKey {
		index.Name = "PRIMARY"
		index.Unique = true
	}
	return index, nil
}
