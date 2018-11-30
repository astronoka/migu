package migu

import (
	"database/sql"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/astronoka/migu/dialect"
	"github.com/azer/snakecase"
	"github.com/naoina/go-stringutil"
)

// Sync synchronizes the schema between Go's struct and the database.
// Go's struct may be provided via the filename of the source file, or via
// the src parameter.
//
// If src != nil, Sync parses the source from src and filename is not used.
// The type of the argument for the src parameter must be string, []byte, or
// io.Reader. If src == nil, Sync parses the file specified by filename.
//
// All query for synchronization will be performed within the transaction if
// storage engine supports the transaction. (e.g. MySQL's MyISAM engine does
// NOT support the transaction)
func Sync(db *sql.DB, filename string, src interface{}) error {
	sqls, err := Diff(db, filename, src)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, sql := range sqls {
		if _, err := tx.Exec(sql); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Diff returns SQLs for schema synchronous between database and Go's struct.
func Diff(db *sql.DB, filename string, src interface{}) ([]string, error) {
	expectedTableASTMap, err := newTableASTsFromFile(filename, src)
	if err != nil {
		return nil, fmt.Errorf("migu: Diff error. " + err.Error())
	}
	currentTableMap, err := newTablesFromDB(db)
	if err != nil {
		return nil, fmt.Errorf("migu: Diff error. " + err.Error())
	}
	d := &dialect.MySQL{}
	var migrations []string
	for _, name := range sortTableASTNames(expectedTableASTMap) {
		tableAST := expectedTableASTMap[name]
		currentTable, alreadyExist := currentTableMap[name]
		if !alreadyExist {
			queries, err := tableAST.CreateTableQuery(d)
			if err != nil {
				return nil, fmt.Errorf("migu: Diff error. " + err.Error())
			}
			migrations = append(migrations, queries...)
		} else {
			queries, err := tableAST.AlterTableQueries(d, currentTable)
			if err != nil {
				return nil, fmt.Errorf("migu: Diff error. " + err.Error())
			}
			migrations = append(migrations, queries...)
		}
		delete(expectedTableASTMap, name)
		delete(currentTableMap, name)
	}
	for name := range currentTableMap {
		migrations = append(migrations, fmt.Sprintf(`DROP TABLE %s`, d.Quote(toSchemaTableName(name))))
	}
	return migrations, nil
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

// Fprint generates Go's structs from database schema and writes to output.
func Fprint(output io.Writer, db *sql.DB) error {
	tableMap, err := newTablesFromDB(db)
	if err != nil {
		return err
	}
	if hasDatetimeColumn(tableMap) {
		if err := fprintln(output, importAST("time")); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(tableMap))
	for name := range tableMap {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		table := tableMap[name]
		err := fprintTable(output, name, table)
		if err != nil {
			return err
		}
		err = fprintIndex(output, name, table)
		if err != nil {
			return err
		}
	}
	return nil
}

func fprintTable(output io.Writer, name string, table *Table) error {
	s, err := structAST(name, table)
	if err != nil {
		return err
	}
	if err := fprintln(output, s); err != nil {
		return err
	}
	return nil
}

func fprintIndex(output io.Writer, tableName string, table *Table) error {
	s, err := indexStructAST(tableName, table.Indexes)
	if err != nil {
		return err
	}
	if err := fprintln(output, s); err != nil {
		return err
	}
	return nil
}

const (
	tagDefault       = "default"
	tagPrimaryKey    = "pk"
	tagAutoIncrement = "autoincrement"
	tagUnique        = "unique"
	tagSize          = "size"
	tagIndex         = "index"
	tagIgnore        = "-"
	tagSeparater     = ";"
)

func formatDefault(d dialect.Dialect, t, def string) string {
	switch t {
	case "string":
		return d.QuoteString(def)
	default:
		return def
	}
}

func fprintln(output io.Writer, decl ast.Decl) error {
	if err := format.Node(output, token.NewFileSet(), decl); err != nil {
		return err
	}
	fmt.Fprintf(output, "\n\n")
	return nil
}

func sortTableASTNames(tableASTMap map[string]*TableAST) []string {
	names := make([]string, 0, len(tableASTMap))
	for name := range tableASTMap {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func detectTypeName(n ast.Node) (string, error) {
	switch t := n.(type) {
	case *ast.Field:
		return detectTypeName(t.Type)
	case *ast.Ident:
		return t.Name, nil
	case *ast.SelectorExpr:
		name, err := detectTypeName(t.X)
		if err != nil {
			return "", err
		}
		return name + "." + t.Sel.Name, nil
	case *ast.StarExpr:
		name, err := detectTypeName(t.X)
		if err != nil {
			return "", err
		}
		return "*" + name, nil
	default:
		return "", fmt.Errorf("migu: BUG: unknown type %T", t)
	}
}

func columnSQL(d dialect.Dialect, f *Column) string {
	colType, null := d.ColumnType(f.Type, f.Size, f.AutoIncrement)
	column := []string{d.Quote(toSchemaFieldName(f.Name)), colType}
	if !null {
		column = append(column, "NOT NULL")
	}
	if f.Default != "" {
		column = append(column, "DEFAULT", formatDefault(d, f.Type, f.Default))
	}
	if f.PrimaryKey {
		column = append(column, "PRIMARY KEY")
	}
	if f.AutoIncrement && d.AutoIncrement() != "" {
		column = append(column, d.AutoIncrement())
	}
	if f.Unique {
		column = append(column, "UNIQUE")
	}
	if f.Comment != "" {
		column = append(column, "COMMENT", d.QuoteString(f.Comment))
	}
	return strings.Join(column, " ")
}

func hasDatetimeColumn(tables map[string]*Table) bool {
	for _, table := range tables {
		if table.HasDatetimeColumn() {
			return true
		}
	}
	return false
}

func importAST(pkg string) ast.Decl {
	return &ast.GenDecl{
		Tok: token.IMPORT,
		Specs: []ast.Spec{
			&ast.ImportSpec{
				Path: &ast.BasicLit{
					Kind:  token.STRING,
					Value: fmt.Sprintf(`"%s"`, pkg),
				},
			},
		},
	}
}

func structAST(name string, table *Table) (ast.Decl, error) {
	var fields []*ast.Field
	for _, schema := range table.Columns {
		f, err := schema.fieldAST()
		if err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
	return &ast.GenDecl{
		Tok: token.TYPE,
		Specs: []ast.Spec{
			&ast.TypeSpec{
				Name: ast.NewIdent(toPublicStructName(name)),
				Type: &ast.StructType{
					Fields: &ast.FieldList{
						List: fields,
					},
				},
			},
		},
	}, nil
}

func indexStructAST(tableName string, indexes []*Index) (ast.Decl, error) {
	names := make([]string, 0, len(indexes))
	indexMap := make(map[string]*Index)
	for _, index := range indexes {
		names = append(names, index.Name)
		indexMap[index.Name] = index
	}
	sort.Strings(names)

	var fields []*ast.Field
	for i, name := range names {
		index := indexMap[name]
		f, err := index.AsASTField(i)
		if err != nil {
			return nil, fmt.Errorf("migu: indexStructAST: " + err.Error())
		}
		fields = append(fields, f)
	}
	return &ast.GenDecl{
		Tok: token.TYPE,
		Specs: []ast.Spec{
			&ast.TypeSpec{
				Name: ast.NewIdent(toPublicStructName(tableName) + "Index"),
				Type: &ast.StructType{
					Fields: &ast.FieldList{
						List: fields,
					},
				},
			},
		},
	}, nil
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

func toPublicStructName(s string) string {
	return stringutil.ToUpperCamelCase(s)
}

func toStructPublicFieldName(s string) string {
	return stringutil.ToUpperCamelCase(s)
}

func toSchemaTableName(s string) string {
	return snakecase.SnakeCase(s)
}

func toSchemaFieldName(s string) string {
	return snakecase.SnakeCase(s)
}
