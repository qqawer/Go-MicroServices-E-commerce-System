// dbtool only manages the marked, isolated integration database; it is not a production migrator.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

const databases = "'grpctest_user_srv','grpctest_goods_srv','grpctest_inventory_srv','grpctest_orders_srv'"
const lockName = "grpctest_isolated_migrations"

type connection struct {
	Host, User, Password, Marker string
	Port                         int
}
type migration struct {
	version         int
	name, hash, sql string
}

func digest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// Never include SQL, DSNs, server messages, or row values in failures.
func databaseError(operation string, err error) error {
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) {
		return fmt.Errorf("%s failed (MySQL error %d)", operation, mysqlError.Number)
	}
	return fmt.Errorf("%s failed", operation)
}

func readMigrations(dir string) ([]migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, errors.New("cannot read migration directory")
	}
	pattern := regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)
	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := pattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, errors.New("invalid migration filename")
		}
		version, _ := strconv.Atoi(match[1])
		if version != len(migrations)+1 {
			return nil, errors.New("migrations must be consecutive from 0001")
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil || strings.TrimSpace(string(data)) == "" {
			return nil, errors.New("cannot read nonempty migration")
		}
		migrations = append(migrations, migration{version, entry.Name(), digest(data), string(data)})
	}
	if len(migrations) == 0 {
		return nil, errors.New("no migrations found")
	}
	return migrations, nil
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: dbtool <up|status|seed> --migrations DIR [--seed FILE]")
	}
	command := os.Args[1]
	if command != "up" && command != "status" && command != "seed" {
		return errors.New("command must be up, status, or seed")
	}
	flags := flag.NewFlagSet("dbtool", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("migrations", "deploy/mysql/migrations", "migration directory")
	seed := flags.String("seed", "", "test seed SQL")
	if err := flags.Parse(os.Args[2:]); err != nil || flags.NArg() != 0 {
		return errors.New("invalid arguments")
	}
	if (command == "seed") != (*seed != "") {
		return errors.New("--seed is required only for seed command")
	}
	migrations, err := readMigrations(*dir)
	if err != nil {
		return err
	}
	var settings connection
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return errors.New("invalid connection JSON on stdin")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("unexpected trailing connection data")
	}
	ip := net.ParseIP(settings.Host)
	if ip == nil || !ip.IsLoopback() || settings.Port < 1 || settings.Port > 65535 || settings.User == "" || settings.Password == "" || settings.Marker == "" {
		return errors.New("require numeric loopback host, valid port, user, password, and environment marker")
	}
	cfg := mysql.NewConfig()
	cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(settings.Host, strconv.Itoa(settings.Port))
	cfg.User, cfg.Passwd, cfg.DBName = settings.User, settings.Password, "grpctest_migrations"
	cfg.MultiStatements, cfg.InterpolateParams = true, true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 90*time.Second, 90*time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return databaseError("open database", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return databaseError("connect database", err)
	}
	defer conn.Close()
	var count int
	var marker string
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(MIN(marker),'') FROM grpctest_migrations.test_environment").Scan(&count, &marker); err != nil {
		return databaseError("verify isolated environment", err)
	}
	if count != 1 || marker != settings.Marker {
		return errors.New("isolated environment marker mismatch")
	}
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 10)", lockName).Scan(&locked); err != nil {
		return databaseError("acquire migration lock", err)
	}
	if !locked.Valid || locked.Int64 != 1 {
		return errors.New("migration lock unavailable")
	}
	defer func() {
		releaseCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_, _ = conn.ExecContext(releaseCtx, "DO RELEASE_LOCK(?)", lockName)
	}()
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME IN ("+databases+")").Scan(&count); err != nil {
		return databaseError("check business databases", err)
	}
	if count != 4 {
		return errors.New("four isolated business databases must exist")
	}
	_, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS grpctest_migrations.schema_migrations (
version INT NOT NULL PRIMARY KEY, filename VARCHAR(255) NOT NULL, sha256 CHAR(64) NOT NULL,
dirty BOOLEAN NOT NULL, applied_at DATETIME(3) NULL) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS grpctest_migrations.seed_history (
id TINYINT NOT NULL PRIMARY KEY, sha256 CHAR(64) NOT NULL, applied_at DATETIME(3) NOT NULL,
CONSTRAINT one_seed CHECK (id = 1)) ENGINE=InnoDB;`)
	if err != nil {
		return databaseError("prepare migration history", err)
	}
	applied, err := checkHistory(ctx, conn, migrations)
	if err != nil {
		return err
	}
	if applied == 0 {
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA IN ("+databases+")").Scan(&count); err != nil {
			return databaseError("check empty baseline", err)
		}
		if count != 0 {
			return errors.New("business databases contain tables without migration history; refusing baseline")
		}
	}
	if command == "status" {
		fmt.Printf("migrations: %d/%d applied, clean\n", applied, len(migrations))
		return nil
	}
	if command == "seed" {
		if applied != len(migrations) {
			return errors.New("seed requires all migrations applied")
		}
		return applySeed(ctx, conn, *seed)
	}
	for _, item := range migrations[applied:] {
		if _, err := conn.ExecContext(ctx, "INSERT INTO grpctest_migrations.schema_migrations (version,filename,sha256,dirty) VALUES (?,?,?,1)", item.version, item.name, item.hash); err != nil {
			return databaseError("record pending migration", err)
		}
		if _, err := conn.ExecContext(ctx, item.sql); err != nil {
			return databaseError(fmt.Sprintf("migration %04d; history remains dirty", item.version), err)
		}
		if _, err := conn.ExecContext(ctx, "UPDATE grpctest_migrations.schema_migrations SET dirty=0,applied_at=NOW(3) WHERE version=?", item.version); err != nil {
			return databaseError("complete migration; history remains dirty", err)
		}
		fmt.Printf("applied %s\n", item.name)
	}
	fmt.Printf("migrations: %d/%d applied, clean\n", len(migrations), len(migrations))
	return nil
}

func checkHistory(ctx context.Context, conn *sql.Conn, migrations []migration) (int, error) {
	rows, err := conn.QueryContext(ctx, "SELECT version,filename,sha256,dirty FROM grpctest_migrations.schema_migrations ORDER BY version")
	if err != nil {
		return 0, databaseError("read migration history", err)
	}
	defer rows.Close()
	applied := 0
	for rows.Next() {
		var version int
		var filename, hash string
		var dirty bool
		if err := rows.Scan(&version, &filename, &hash, &dirty); err != nil {
			return 0, databaseError("read migration history", err)
		}
		if version != applied+1 || version > len(migrations) {
			return 0, errors.New("unknown or nonconsecutive migration history")
		}
		item := migrations[applied]
		if filename != item.name || hash != item.hash {
			return 0, errors.New("applied migration filename or checksum changed")
		}
		if dirty {
			return 0, errors.New("dirty migration history; inspect failure or reset the isolated test environment")
		}
		applied++
	}
	if err := rows.Err(); err != nil {
		return 0, databaseError("read migration history", err)
	}
	return applied, nil
}

func applySeed(ctx context.Context, conn *sql.Conn, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("cannot read seed file")
	}
	statements, err := seedStatements(string(data))
	if err != nil {
		return err
	}
	hash := digest(data)
	var previous string
	err = conn.QueryRowContext(ctx, "SELECT sha256 FROM grpctest_migrations.seed_history WHERE id=1").Scan(&previous)
	if err == nil {
		if previous != hash {
			return errors.New("seed checksum changed; reset the isolated test environment to replace fixtures")
		}
		fmt.Println("seed already applied; existing data unchanged")
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return databaseError("read seed history", err)
	}
	rows, err := conn.QueryContext(ctx, "SELECT TABLE_SCHEMA,TABLE_NAME,COALESCE(ENGINE,'') FROM information_schema.TABLES WHERE TABLE_SCHEMA IN ("+databases+")")
	if err != nil {
		return databaseError("inspect seed target tables", err)
	}
	var tables []string
	for rows.Next() {
		var schema, table, engine string
		if err := rows.Scan(&schema, &table, &engine); err != nil {
			rows.Close()
			return databaseError("inspect seed target tables", err)
		}
		if engine != "InnoDB" {
			rows.Close()
			return errors.New("seed requires only transactional InnoDB business tables")
		}
		tables = append(tables, "`"+strings.ReplaceAll(schema, "`", "``")+"`.`"+strings.ReplaceAll(table, "`", "``")+"`")
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return databaseError("inspect seed target tables", err)
	}
	if len(tables) == 0 {
		return errors.New("seed requires migrated business tables")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return databaseError("begin seed transaction", err)
	}
	defer tx.Rollback()
	for _, table := range tables {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return databaseError("check empty seed target", err)
		}
		if count != 0 {
			return errors.New("first seed requires every business table empty")
		}
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return databaseError("seed transaction (rolled back)", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO grpctest_migrations.seed_history (id,sha256,applied_at) VALUES (1,?,NOW(3))", hash); err != nil {
		return databaseError("record seed transaction", err)
	}
	if err := tx.Commit(); err != nil {
		return databaseError("commit seed; inspect history before retrying", err)
	}
	fmt.Println("seed applied")
	return nil
}

// Split outside SQL literals/comments, then permit INSERT only. DDL would implicitly
// commit a MySQL transaction and must never be accepted as a seed operation.
func seedStatements(source string) ([]string, error) {
	var statements []string
	var buffer strings.Builder
	var quote byte
	for i := 0; i < len(source); i++ {
		c := source[i]
		if quote != 0 {
			buffer.WriteByte(c)
			if c == '\\' && i+1 < len(source) {
				i++
				buffer.WriteByte(source[i])
				continue
			}
			if c == quote {
				if i+1 < len(source) && source[i+1] == quote {
					i++
					buffer.WriteByte(source[i])
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			buffer.WriteByte(c)
			continue
		}
		if c == '#' || (c == '-' && i+2 < len(source) && source[i+1] == '-' && source[i+2] <= ' ') {
			for i < len(source) && source[i] != '\n' {
				i++
			}
			buffer.WriteByte(' ')
			continue
		}
		if c == '/' && i+1 < len(source) && source[i+1] == '*' {
			end := strings.Index(source[i+2:], "*/")
			if end < 0 || (i+2 < len(source) && source[i+2] == '!') {
				return nil, errors.New("unsupported seed comment")
			}
			i += end + 3
			buffer.WriteByte(' ')
			continue
		}
		if c == ';' {
			if text := strings.TrimSpace(buffer.String()); text != "" {
				statements = append(statements, text)
			}
			buffer.Reset()
			continue
		}
		buffer.WriteByte(c)
	}
	if quote != 0 {
		return nil, errors.New("unclosed seed literal")
	}
	if text := strings.TrimSpace(buffer.String()); text != "" {
		statements = append(statements, text)
	}
	pattern := regexp.MustCompile("(?is)^INSERT\\s+INTO\\s+`?grpctest_(user|goods|inventory|orders)_srv`?\\s*\\.\\s*`?[a-z_]+`?\\s*(\\(|VALUES\\b|SELECT\\b)")
	if len(statements) == 0 {
		return nil, errors.New("empty seed")
	}
	for _, statement := range statements {
		if !pattern.MatchString(statement) {
			return nil, errors.New("seed permits only INSERT into isolated business databases")
		}
	}
	return statements, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dbtool:", err)
		os.Exit(1)
	}
}
