package main

import (
	"database/sql"
	"fmt"

	"github.com/astronoka/migu"
	_ "github.com/go-sql-driver/mysql"
)

func main() {
	db, err := sql.Open("mysql", "user@/migu_test")
	if err != nil {
		panic(err)
	}
	defer db.Close()
	migrations, err := migu.Diff(db, "schema.go", nil)
	if err != nil {
		panic(err)
	}
	for _, m := range migrations {
		fmt.Printf("%v\n", m)
	}
}
