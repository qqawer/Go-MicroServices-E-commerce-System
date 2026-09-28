package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"grpctest/order_srv/handler"
)

const fixtureConfig = `{"mysql":{"host":"localhost","port":"3306","user":"operator","password":"secret-password-fixture","name":"grpctest_orders_srv"},"alipay":{"enabled":true,"appid":"app-fixture","private_key":"secret-private-key-fixture"}}`

type fakeReviewService struct {
	inspected, fenced int
	id                int32
	request           handler.FenceCloseRequest
	err               error
}

func (f *fakeReviewService) Inspect(_ context.Context, id int32) (handler.CloseReviewInfo, error) {
	f.inspected++
	f.id = id
	return handler.CloseReviewInfo{OrderID: id, Status: "CLOSING"}, f.err
}

func (f *fakeReviewService) Fence(_ context.Context, req handler.FenceCloseRequest) (handler.CloseReviewInfo, error) {
	f.fenced++
	f.request = req
	return handler.CloseReviewInfo{OrderID: req.OrderID, Status: "TRADE_CLOSED"}, f.err
}

func fenceArgs(execute bool) []string {
	args := []string{"fence-and-close", "--order-id", "1", "--request-id", "close-test-1", "--operator", "test.operator", "--buyer-id", "2088000000000002"}
	if execute {
		args = append(args, "--execute")
	}
	return args
}

func TestReviewDryRunNeverCallsFence(t *testing.T) {
	for _, args := range [][]string{{"inspect", "--order-id", "1"}, fenceArgs(false)} {
		service := &fakeReviewService{}
		cleanup := false
		open := func(_ context.Context, cfg privateConfig, execute bool) (reviewService, func(), error) {
			if execute || cfg.Mysql.Password != "secret-password-fixture" {
				t.Fatal("dry run requested mutation or lost private input")
			}
			return service, func() { cleanup = true }, nil
		}
		var output bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader(fixtureConfig), &output, open); err != nil {
			t.Fatal(err)
		}
		if service.inspected != 1 || service.fenced != 0 || service.id != 1 || !cleanup {
			t.Fatalf("unexpected dry run calls: %+v", service)
		}
		if strings.Contains(output.String(), "secret-") || strings.Contains(output.String(), "2088000000000002") {
			t.Fatal("private inputs leaked into dry-run output")
		}
		var result struct {
			DryRun bool `json:"dry_run"`
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil || !result.DryRun {
			t.Fatalf("invalid dry-run result: %v", err)
		}
	}
}

func TestReviewExecuteCallsOnlyExplicitFenceWithOriginalInputs(t *testing.T) {
	service := &fakeReviewService{}
	var output bytes.Buffer
	open := func(_ context.Context, _ privateConfig, execute bool) (reviewService, func(), error) {
		if !execute {
			t.Fatal("execute was lost")
		}
		return service, func() {}, nil
	}
	if err := run(context.Background(), fenceArgs(true), strings.NewReader(fixtureConfig), &output, open); err != nil {
		t.Fatal(err)
	}
	if service.inspected != 0 || service.fenced != 1 || service.request.OrderID != 1 || service.request.RequestID != "close-test-1" || service.request.Operator != "test.operator" || service.request.BuyerID != "2088000000000002" {
		t.Fatal("execution changed the operator's original input")
	}
	if strings.Contains(output.String(), "2088000000000002") || strings.Contains(output.String(), "secret-") || !strings.Contains(output.String(), `"dry_run":false`) {
		t.Fatal("unsafe execute output")
	}
}

func TestReviewRejectsInvalidArgumentsBeforeOpeningAnything(t *testing.T) {
	for _, args := range [][]string{
		nil, {"other"}, {"inspect"}, {"inspect", "--order-id", "0"}, {"inspect", "--order-id", "2147483648"},
		{"inspect", "--order-id", "1", "--execute"}, {"inspect", "--order-id", "1", "--buyer-id", "2088000000000002"},
		{"inspect", "--order-id", "1", "extra"}, {"inspect", "--order-id", "secret-flag-value"},
		{"fence-and-close", "--order-id", "1", "--execute"},
		append(fenceArgs(true), "--buyer-id", "3088000000000002"),
		append(fenceArgs(true), "--request-id", "invalid.request"),
	} {
		err := run(context.Background(), args, strings.NewReader(fixtureConfig), io.Discard, func(context.Context, privateConfig, bool) (reviewService, func(), error) {
			t.Fatal("invalid arguments opened runtime")
			return nil, nil, nil
		})
		if err == nil || strings.Contains(err.Error(), "secret-flag-value") {
			t.Fatalf("unsafe argument handling for %v: %v", args, err)
		}
	}
}

func TestReviewErrorsNeverEchoDriverChannelOrPrivateConfig(t *testing.T) {
	privateError := errors.New("dsn-secret-password-fixture and channel-private-key-fixture")
	for _, setupFailure := range []bool{true, false} {
		open := func(context.Context, privateConfig, bool) (reviewService, func(), error) {
			if setupFailure {
				return nil, nil, privateError
			}
			return &fakeReviewService{err: privateError}, func() {}, nil
		}
		var output bytes.Buffer
		err := run(context.Background(), fenceArgs(true), strings.NewReader(fixtureConfig), &output, open)
		if err == nil || strings.Contains(err.Error(), "fixture") || output.Len() != 0 {
			t.Fatalf("unsafe error/output: %v", err)
		}
	}
}

func TestReviewConfigIsBoundedStrictAndOrdersDatabaseOnly(t *testing.T) {
	for _, body := range []string{
		"", "null", `{"mysql":"secret-private-value"}`, fixtureConfig + `{}`, fixtureConfig + ` secret-private-value`,
		strings.Replace(fixtureConfig, "grpctest_orders_srv", "grpctest_inventory_srv", 1),
		strings.Replace(fixtureConfig, `"3306"`, `"0"`, 1),
		strings.Replace(fixtureConfig, `"3306"`, `"65536"`, 1),
		strings.Replace(fixtureConfig, `"localhost"`, `" "`, 1),
		strings.Replace(fixtureConfig, `"enabled":true`, `"enabled":true,"unknown":"secret-private-value"`, 1),
		strings.Repeat("x", 64*1024+1),
	} {
		if _, err := readConfig(strings.NewReader(body)); err == nil || strings.Contains(err.Error(), "secret-") {
			t.Fatalf("unsafe configuration handling: %v", err)
		}
	}
	if _, err := readConfig(strings.NewReader(fixtureConfig + "\n")); err != nil {
		t.Fatal(err)
	}
}

func TestReviewHelpDoesNotReadSecretsOrOpenRuntime(t *testing.T) {
	var output bytes.Buffer
	err := run(context.Background(), []string{"--help"}, nil, &output, func(context.Context, privateConfig, bool) (reviewService, func(), error) {
		t.Fatal("help opened runtime")
		return nil, nil, nil
	})
	if err != nil || !strings.Contains(output.String(), "Without --execute") {
		t.Fatal("help did not explain dry run")
	}
}

func TestRequireReviewSchemaRejectsMissingMigration(t *testing.T) {
	columns := []string{"order_sn", "reason", "attempts", "first_seen_at", "last_checked_at", "next_check_at", "resolved_at", "resolution", "request_id", "operator", "request_hash", "action_started_at", "action_expires_at", "app_id", "seller_id"}
	for _, count := range []int{0, len(columns) - 1, len(columns)} {
		conn, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		db, err := gorm.Open(mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		rows := sqlmock.NewRows([]string{"COLUMN_NAME"})
		for _, column := range columns[:count] {
			rows.AddRow(column)
		}
		mock.ExpectQuery("SELECT COLUMN_NAME FROM information_schema.COLUMNS").WithArgs(ordersDatabase, "order_close_review").WillReturnRows(rows)
		err = requireReviewSchema(context.Background(), db)
		if (err == nil) != (count == len(columns)) {
			t.Fatalf("schema with %d columns: %v", count, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}
}

func TestDatabaseConfigUsesAWSUTCRegardlessOfOperatorTimeZone(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("operator-laptop", 8*60*60)
	t.Cleanup(func() { time.Local = previous })
	cfg, err := readConfig(strings.NewReader(fixtureConfig))
	if err != nil {
		t.Fatal(err)
	}
	dsn := databaseConfig(cfg.Mysql)
	parsed, err := mysqldriver.ParseDSN(dsn.FormatDSN())
	if err != nil {
		t.Fatal("DSN could not be parsed")
	}
	if !parsed.ParseTime || parsed.Loc != time.UTC || parsed.DBName != ordersDatabase {
		t.Fatal("operator's local timezone changed UTC database interpretation")
	}
	value, err := time.ParseInLocation("2006-01-02 15:04:05", "2026-09-28 10:30:00", parsed.Loc)
	if err != nil || value.Format(time.RFC3339) != "2026-09-28T10:30:00Z" {
		t.Fatal("UTC database timestamp was shifted")
	}
}
