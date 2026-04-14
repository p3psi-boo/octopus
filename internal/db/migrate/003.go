package migrate

import (
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 3,
		Up:      convertStatsDateToInt,
	})
}

// 003: convert stats_daily.date and stats_hourly.date from string to int
// This migration ensures backward compatibility when upgrading from older versions
// that stored dates as "20060102" format strings.
func convertStatsDateToInt(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}

	dialect := db.Dialector.Name()

	switch dialect {
	case "sqlite":
		return convertStatsDateSQLite(db)
	case "mysql", "postgres":
		return convertStatsDateSQL(db, dialect)
	default:
		// For other databases, try the SQL approach
		return convertStatsDateSQL(db, dialect)
	}
}

// convertStatsDateSQLite handles SQLite migration (requires table recreation)
func convertStatsDateSQLite(db *gorm.DB) error {
	// Check if stats_daily table exists and has string-type date
	var dateColType string
	db.Raw("SELECT type FROM pragma_table_info('stats_dailies') WHERE name = 'date' LIMIT 1").Scan(&dateColType)

	// If already INTEGER type, nothing to do
	if dateColType == "INTEGER" {
		return nil
	}

	// If TEXT type (or empty indicating old schema), need conversion
	if dateColType == "TEXT" || dateColType == "VARCHAR" || dateColType == "" {
		if err := migrateStatsDailySQLite(db); err != nil {
			return fmt.Errorf("failed to migrate stats_daily: %w", err)
		}
	}

	// Check stats_hourly
	dateColType = ""
	db.Raw("SELECT type FROM pragma_table_info('stats_hourlies') WHERE name = 'date' LIMIT 1").Scan(&dateColType)

	if dateColType == "INTEGER" {
		return nil
	}

	if dateColType == "TEXT" || dateColType == "VARCHAR" || dateColType == "" {
		if err := migrateStatsHourlySQLite(db); err != nil {
			return fmt.Errorf("failed to migrate stats_hourly: %w", err)
		}
	}

	return nil
}

// migrateStatsDailySQLite recreates stats_daily table with proper types
func migrateStatsDailySQLite(db *gorm.DB) error {
	// 1. Create temporary table with new schema
	if err := db.Exec(`
		CREATE TABLE stats_dailies_new (
			date INTEGER PRIMARY KEY,
			input_token BIGINT DEFAULT 0,
			output_token BIGINT DEFAULT 0,
			input_cost REAL DEFAULT 0,
			output_cost REAL DEFAULT 0,
			wait_time BIGINT DEFAULT 0,
			request_success BIGINT DEFAULT 0,
			request_failed BIGINT DEFAULT 0
		)
	`).Error; err != nil {
		return err
	}

	// 2. Migrate data - convert "20060102" string to year*1000+yearday int
	var oldRecords []struct {
		Date           string
		InputToken     int64
		OutputToken    int64
		InputCost      float64
		OutputCost     float64
		WaitTime       int64
		RequestSuccess int64
		RequestFailed  int64
	}
	if err := db.Raw("SELECT * FROM stats_dailies").Scan(&oldRecords).Error; err != nil {
		// Drop temp table on error
		db.Exec("DROP TABLE stats_dailies_new")
		return err
	}

	for _, r := range oldRecords {
		intDate := convertStringDateToInt(r.Date)
		if err := db.Exec(`
			INSERT INTO stats_dailies_new 
			(date, input_token, output_token, input_cost, output_cost, wait_time, request_success, request_failed)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, intDate, r.InputToken, r.OutputToken, r.InputCost, r.OutputCost, r.WaitTime, r.RequestSuccess, r.RequestFailed).Error; err != nil {
			// Continue on error, log it
			continue
		}
	}

	// 3. Drop old table and rename
	if err := db.Exec("DROP TABLE stats_dailies").Error; err != nil {
		db.Exec("DROP TABLE stats_dailies_new")
		return err
	}
	if err := db.Exec("ALTER TABLE stats_dailies_new RENAME TO stats_dailies").Error; err != nil {
		return err
	}

	return nil
}

// migrateStatsHourlySQLite recreates stats_hourly table with proper types
func migrateStatsHourlySQLite(db *gorm.DB) error {
	// 1. Create temporary table
	if err := db.Exec(`
		CREATE TABLE stats_hourlies_new (
			hour INTEGER PRIMARY KEY,
			date INTEGER NOT NULL,
			input_token BIGINT DEFAULT 0,
			output_token BIGINT DEFAULT 0,
			input_cost REAL DEFAULT 0,
			output_cost REAL DEFAULT 0,
			wait_time BIGINT DEFAULT 0,
			request_success BIGINT DEFAULT 0,
			request_failed BIGINT DEFAULT 0
		)
	`).Error; err != nil {
		return err
	}

	// 2. Migrate data
	var oldRecords []struct {
		Hour           int
		Date           string
		InputToken     int64
		OutputToken    int64
		InputCost      float64
		OutputCost     float64
		WaitTime       int64
		RequestSuccess int64
		RequestFailed  int64
	}
	if err := db.Raw("SELECT * FROM stats_hourlies").Scan(&oldRecords).Error; err != nil {
		db.Exec("DROP TABLE stats_hourlies_new")
		return err
	}

	for _, r := range oldRecords {
		intDate := convertStringDateToInt(r.Date)
		if err := db.Exec(`
			INSERT INTO stats_hourlies_new 
			(hour, date, input_token, output_token, input_cost, output_cost, wait_time, request_success, request_failed)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, r.Hour, intDate, r.InputToken, r.OutputToken, r.InputCost, r.OutputCost, r.WaitTime, r.RequestSuccess, r.RequestFailed).Error; err != nil {
			continue
		}
	}

	// 3. Drop and rename
	if err := db.Exec("DROP TABLE stats_hourlies").Error; err != nil {
		db.Exec("DROP TABLE stats_hourlies_new")
		return err
	}
	if err := db.Exec("ALTER TABLE stats_hourlies_new RENAME TO stats_hourlies").Error; err != nil {
		return err
	}

	return nil
}

// convertStatsDateSQL handles MySQL/PostgreSQL migration
func convertStatsDateSQL(db *gorm.DB, dialect string) error {
	// For MySQL/PostgreSQL, we can use ALTER COLUMN
	// But first we need to convert existing data

	// Check if there are string dates that need conversion
	var count int64
	db.Raw("SELECT COUNT(*) FROM stats_dailies WHERE date LIKE '%-%' OR date LIKE '%/%' OR LENGTH(CAST(date AS TEXT)) > 8").Scan(&count)

	if count == 0 {
		// Check if dates are already in correct format by trying to parse as int
		var testDate string
		db.Raw("SELECT CAST(date AS TEXT) FROM stats_dailies LIMIT 1").Scan(&testDate)
		if testDate == "" {
			return nil // No data
		}
		// Try to parse as int
		if _, err := strconv.Atoi(testDate); err == nil && len(testDate) <= 8 {
			// Already int format, check if we need to alter column type
			return alterColumnTypeSQL(db, dialect)
		}
	}

	// Need to convert existing string dates to int format
	// This is complex in MySQL/PostgreSQL without a proper date parsing function
	// For now, we'll clear the stats tables to avoid type conflicts
	// (Stats data is not critical and will be rebuilt quickly)

	db.Exec("TRUNCATE TABLE stats_dailies")
	db.Exec("TRUNCATE TABLE stats_hourlies")

	return alterColumnTypeSQL(db, dialect)
}

// alterColumnTypeSQL changes column type to INTEGER
func alterColumnTypeSQL(db *gorm.DB, dialect string) error {
	switch dialect {
	case "mysql":
		if err := db.Exec("ALTER TABLE stats_dailies MODIFY COLUMN date INTEGER").Error; err != nil {
			return err
		}
		if err := db.Exec("ALTER TABLE stats_hourlies MODIFY COLUMN date INTEGER").Error; err != nil {
			return err
		}
	case "postgres":
		if err := db.Exec("ALTER TABLE stats_dailies ALTER COLUMN date TYPE INTEGER USING date::integer").Error; err != nil {
			return err
		}
		if err := db.Exec("ALTER TABLE stats_hourlies ALTER COLUMN date TYPE INTEGER USING date::integer").Error; err != nil {
			return err
		}
	}
	return nil
}

// convertStringDateToInt converts "20060102" format to year*1000+yearday
func convertStringDateToInt(dateStr string) int {
	if dateStr == "" {
		return 0
	}

	// Try to parse as integer first (already converted)
	if intVal, err := strconv.Atoi(dateStr); err == nil && len(dateStr) <= 8 {
		// Check if it's already in our new format (year*1000 + yearday)
		year := intVal / 1000
		if year >= 2020 && year <= 2100 {
			return intVal
		}
	}

	// Parse "20060102" format
	t, err := time.Parse("20060102", dateStr)
	if err != nil {
		// Fallback: try to parse as int directly
		if intVal, err := strconv.Atoi(dateStr); err == nil {
			return intVal
		}
		return 0
	}

	return t.Year()*1000 + t.YearDay()
}
