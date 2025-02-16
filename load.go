package fixtures

import (
	"database/sql"
	"fmt"
	"io/ioutil"
	"strings"

	"gopkg.in/yaml.v2"
)

// NewProcessingError ...
func NewProcessingError(row int, cause error) error {
	return fmt.Errorf("Error loading row %d: %s", row, cause.Error())
}

// NewFileError ...
func NewFileError(filename string, cause error) error {
	return fmt.Errorf("Error loading file %s: %s", filename, cause.Error())
}

// Load processes a YAML fixture and inserts/updates the database accordingly
func Load(data []byte, db *sql.DB, driver string, oneTransactionPerRow ...bool) error {
	// Unmarshal the YAML data into a []Row slice
	var rows []Row
	if err := yaml.Unmarshal(data, &rows); err != nil {
		return err
	}

	doOneTransactionPerRow := len(oneTransactionPerRow) > 0 && oneTransactionPerRow[0]

	var tx *sql.Tx
	if !doOneTransactionPerRow {
		// Begin a transaction
		var err error
		tx, err = db.Begin()
		if err != nil {
			return err
		}
	}

	// Iterate over rows define in the fixture
	for i, row := range rows {
		if doOneTransactionPerRow {
			var err error
			tx, err = db.Begin()
			if err != nil {
				return err
			}
		}

		// Load struct variables
		row.Init()
		s := strings.Split(row.Table, ".")
		switch {
			case len(s) > 2:
				return fmt.Errorf("Table name wrong format in yaml")
			case len(s) == 2:
				q := fmt.Sprintf(`SET LOCAL SEARCH_PATH TO %s`, s[0])
				_, err := tx.Exec(q)
				if err != nil {
					tx.Rollback() // rollback the transaction
					return NewProcessingError(i+1, err)
				}
				row.Table = s[1]
			case len(s) == 1:
				// table name without schema, do nothing
			default:
				return fmt.Errorf("Table nmae is empty in yaml")
		}

		// Run a SELECT query to find out if we need to insert or UPDATE
		selectQuery := fmt.Sprintf(
			`SELECT COUNT(*) FROM "%s" WHERE %s`,
			row.Table,
			row.GetWhere(driver, 0),
		)
		var count int
		if err := tx.QueryRow(selectQuery, row.GetPKValues()...).Scan(&count); err != nil {
			tx.Rollback() // rollback the transaction
			return NewProcessingError(i+1, err)
		}

		if count == 0 {
			// Primary key not found, let's run an INSERT query
			insertQuery := fmt.Sprintf(
				`INSERT INTO "%s"(%s) VALUES(%s)`,
				row.Table,
				strings.Join(row.GetInsertColumns(), ", "),
				strings.Join(row.GetInsertPlaceholders(driver), ", "),
			)
			_, err := tx.Exec(insertQuery, row.GetInsertValues()...)
			if err != nil {
				tx.Rollback() // rollback the transaction
				return NewProcessingError(i+1, err)
			}
			if driver == postgresDriver && row.GetInsertColumns()[0] == "\"id\"" {
				err = fixPostgresPKSequence(tx, row.Table, "id")
				if err != nil {
					tx.Rollback()
					return NewProcessingError(i+1, err)
				}
			}
		} else {
			// Primary key found, let's run UPDATE query
			updateQuery := fmt.Sprintf(
				`UPDATE "%s" SET %s WHERE %s`,
				row.Table,
				strings.Join(row.GetUpdatePlaceholders(driver), ", "),
				row.GetWhere(driver, row.GetUpdateValuesLength()),
			)
			values := append(row.GetUpdateValues(), row.GetPKValues()...)
			_, err := tx.Exec(updateQuery, values...)
			if err != nil {
				tx.Rollback() // rollback the transaction
				return NewProcessingError(i+1, err)
			}
			if driver == postgresDriver && row.GetUpdateColumns()[0] == "\"id\"" {
				err = fixPostgresPKSequence(tx, row.Table, "id")
				if err != nil {
					tx.Rollback()
					return NewProcessingError(i+1, err)
				}
			}
		}

		if doOneTransactionPerRow {
			// Commit the transaction
			if err := tx.Commit(); err != nil {
				tx.Rollback() // rollback the transaction
				return err
			}
		}
	}

	if !doOneTransactionPerRow {
		if err := tx.Commit(); err != nil {
			tx.Rollback() // rollback the transaction
			return err
		}
	}

	return nil
}

// LoadFile ...
func LoadFile(filename string, db *sql.DB, driver string) error {
	// Read fixture data from the file
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return NewFileError(filename, err)
	}

	// Insert the fixture data
	return Load(data, db, driver)
}

// LoadFiles ...
func LoadFiles(filenames []string, db *sql.DB, driver string) error {
	for _, filename := range filenames {
		if err := LoadFile(filename, db, driver); err != nil {
			return err
		}
	}
	return nil
}

// fixPostgresPKSequence
func fixPostgresPKSequence(tx *sql.Tx, table string, column string) error {
	// Query for the qualified sequence name
	var seqName *string
	err := tx.QueryRow(`
		SELECT pg_get_serial_sequence($1, $2)
	`, table, column).Scan(&seqName)

	if err != nil {
		return err
	}

	if seqName == nil {
		// No sequence to fix
		return nil
	}

	// Set the sequence
	_, err = tx.Exec(fmt.Sprintf(`
		SELECT pg_catalog.setval($1, (SELECT MAX("%s") FROM "%s"))
	`, column, table), *seqName)

	return err
}
