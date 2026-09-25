package handler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"grpctest/user_srv/model/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	messageCountSQL  = "SELECT count(*) FROM `user_message` WHERE user_id = ? AND `user_message`.`deleted_at` IS NULL"
	messagePageSQL   = "SELECT * FROM `user_message` WHERE user_id = ? AND `user_message`.`deleted_at` IS NULL ORDER BY id DESC LIMIT ?"
	messageLookupSQL = "SELECT * FROM `user_message` WHERE (id = ? AND user_id = ?) AND `user_message`.`deleted_at` IS NULL ORDER BY `user_message`.`id` LIMIT ?"
	messageOwnerSQL  = "SELECT `id` FROM `user` WHERE `user`.`id` = ? AND `user`.`deleted_at` IS NULL ORDER BY `user`.`id` LIMIT ? FOR SHARE"
	messageInsertSQL = "INSERT INTO `user_message` (`add_time`,`update_time`,`deleted_at`,`is_deleted`,`user_id`,`message_type`,`subject`,`message`) VALUES (?,?,?,?,?,?,?,?)"
	messageDeleteSQL = "UPDATE `user_message` SET `deleted_at`=? WHERE (id = ? AND user_id = ?) AND `user_message`.`deleted_at` IS NULL"
)

func TestMessagesRejectInvalidIdentityBeforeDatabaseAccess(t *testing.T) {
	profileMockDB(t)
	server := &MessagesServer{}
	for _, req := range []*proto.MessageLookup{nil, {}, {UserId: 41}, {Id: 17}, {UserId: -1, Id: 17}, {UserId: 41, Id: -1}} {
		if _, err := server.GetMessage(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("GetMessage(%v) must reject invalid identity: %v", req, err)
		}
		if _, err := server.DeleteMessage(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("DeleteMessage(%v) must reject invalid identity: %v", req, err)
		}
	}
	for _, req := range []*proto.MessageListRequest{nil, {}, {UserId: -1}, {UserId: 41, Pn: -1}, {UserId: 41, Psize: -1}, {UserId: 41, Psize: 101}, {UserId: 41, Pn: math.MaxInt32, Psize: 100}} {
		if _, err := server.ListMessages(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("ListMessages(%v) must reject invalid owner or pagination: %v", req, err)
		}
	}
	for _, req := range []*proto.CreateMessageRequest{nil, {}, {UserId: -1, MessageType: 1, Subject: "主题", Message: "内容"}} {
		if _, err := server.CreateMessage(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("CreateMessage(%v) must reject invalid owner: %v", req, err)
		}
	}
}

func TestCreateMessageRejectsInvalidContentBeforeDatabaseAccess(t *testing.T) {
	profileMockDB(t)
	for _, tc := range []struct {
		name        string
		messageType int32
		subject     string
		message     string
	}{
		{name: "zero type", subject: "主题", message: "内容"},
		{name: "negative type", messageType: -1, subject: "主题", message: "内容"},
		{name: "unknown type", messageType: 6, subject: "主题", message: "内容"},
		{name: "empty subject", messageType: 1, message: "内容"},
		{name: "blank subject", messageType: 1, subject: " \t\n\u3000", message: "内容"},
		{name: "empty message", messageType: 1, subject: "主题"},
		{name: "blank message", messageType: 1, subject: "主题", message: " \t\n\u3000"},
		{name: "long subject", messageType: 1, subject: strings.Repeat("中", 101), message: "内容"},
		{name: "long message", messageType: 1, subject: "主题", message: strings.Repeat("中", 2001)},
		{name: "invalid subject UTF8", messageType: 1, subject: string([]byte{0xff}), message: "内容"},
		{name: "invalid message UTF8", messageType: 1, subject: "主题", message: string([]byte{0xff})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := (&MessagesServer{}).CreateMessage(context.Background(), &proto.CreateMessageRequest{
				UserId: 41, MessageType: tc.messageType, Subject: tc.subject, Message: tc.message,
			})
			if response != nil || status.Code(err) != codes.InvalidArgument {
				t.Fatalf("invalid content must fail before database access: %v / %v", response, err)
			}
		})
	}
}

func TestCreateMessageTrimsTextAndAcceptsUnicodeLimitsAndAllTypes(t *testing.T) {
	for messageType := int32(1); messageType <= 5; messageType++ {
		t.Run(string(rune('0'+messageType)), func(t *testing.T) {
			mock := profileMockDB(t)
			subject, body := strings.Repeat("题", 100), strings.Repeat("🙂", 2000)
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(messageOwnerSQL)).WithArgs(41, 1).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
			mock.ExpectExec(regexp.QuoteMeta(messageInsertSQL)).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), nil, false, 41, messageType, subject, body).WillReturnResult(sqlmock.NewResult(17, 1))
			mock.ExpectCommit()
			req := &proto.CreateMessageRequest{UserId: 41, MessageType: messageType, Subject: " \t" + subject + "\u3000", Message: "\n" + body + " "}
			response, err := (&MessagesServer{}).CreateMessage(context.Background(), req)
			if err != nil || response.Id != 17 || response.UserId != 41 || response.MessageType != messageType || response.Subject != subject || response.Message != body || response.CreatedAt <= 0 {
				t.Fatalf("unexpected created message: %v / %v", response, err)
			}
			if req.Subject == subject || req.Message == body {
				t.Fatal("normalization must not mutate the request")
			}
		})
	}
}

func TestCreateMessageDatabaseFailuresAndMissingOwner(t *testing.T) {
	failure := errors.New("private database detail")
	for _, phase := range []string{"begin", "owner", "missing owner", "insert", "commit"} {
		t.Run(phase, func(t *testing.T) {
			mock := profileMockDB(t)
			begin := mock.ExpectBegin()
			if phase == "begin" {
				begin.WillReturnError(failure)
			} else {
				query := mock.ExpectQuery(regexp.QuoteMeta(messageOwnerSQL)).WithArgs(41, 1)
				switch phase {
				case "owner":
					query.WillReturnError(failure)
				case "missing owner":
					query.WillReturnRows(sqlmock.NewRows([]string{"id"}))
				default:
					query.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
					insert := mock.ExpectExec(regexp.QuoteMeta(messageInsertSQL)).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), nil, false, 41, 1, "主题", "内容")
					if phase == "insert" {
						insert.WillReturnError(failure)
					} else {
						insert.WillReturnResult(sqlmock.NewResult(17, 1))
					}
				}
				if phase == "commit" {
					mock.ExpectCommit().WillReturnError(failure)
				} else {
					mock.ExpectRollback()
				}
			}
			response, err := (&MessagesServer{}).CreateMessage(context.Background(), &proto.CreateMessageRequest{UserId: 41, MessageType: 1, Subject: "主题", Message: "内容"})
			want := codes.Internal
			if phase == "missing owner" {
				want = codes.NotFound
			}
			if response != nil || status.Code(err) != want || (want == codes.Internal && status.Convert(err).Message() != "留言操作失败") {
				t.Fatalf("expected safe %s error, got %v / %v", want, response, err)
			}
		})
	}
}

func TestListMessagesScopesOwnerAndActiveRowsWithStablePagination(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pn     int32
		size   int32
		empty  bool
		offset int
	}{
		{name: "defaults"},
		{name: "second page", pn: 2, size: 2, offset: 2},
		{name: "maximum page size", pn: 1, size: 100},
		{name: "empty page retains total", pn: 2, size: 10, offset: 10, empty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := profileMockDB(t)
			created := time.Date(2026, 9, 25, 1, 2, 3, 0, time.UTC)
			rows := sqlmock.NewRows([]string{"id", "user_id", "message_type", "subject", "message", "add_time"})
			if !tc.empty {
				rows.AddRow(18, 41, 5, "主题二", "内容二", created).AddRow(17, 41, 1, "主题一", "内容一", created)
			}
			size := tc.size
			if size == 0 {
				size = 10
			}
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(messageCountSQL)).WithArgs(41).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
			if tc.offset == 0 {
				mock.ExpectQuery(regexp.QuoteMeta(messagePageSQL)).WithArgs(41, size).WillReturnRows(rows)
			} else {
				mock.ExpectQuery(regexp.QuoteMeta(messagePageSQL+" OFFSET ?")).WithArgs(41, size, tc.offset).WillReturnRows(rows)
			}
			mock.ExpectCommit()
			response, err := (&MessagesServer{}).ListMessages(context.Background(), &proto.MessageListRequest{UserId: 41, Pn: tc.pn, Psize: tc.size})
			if err != nil || response.Total != 4 || response.Data == nil {
				t.Fatalf("unexpected page: %v / %v", response, err)
			}
			if tc.empty {
				if len(response.Data) != 0 {
					t.Fatal("empty page must contain no messages")
				}
			} else if len(response.Data) != 2 || response.Data[0].Id != 18 || response.Data[1].Id != 17 || response.Data[0].UserId != 41 || response.Data[0].CreatedAt != created.Unix() || response.Data[0].MessageType != 5 || response.Data[0].Subject != "主题二" || response.Data[0].Message != "内容二" {
				t.Fatal("message mapping or descending page order changed")
			}
		})
	}
}

func TestListMessagesDatabaseFailuresAndTotalOverflow(t *testing.T) {
	failure := errors.New("private database detail")
	for _, phase := range []string{"begin", "count", "find", "commit", "total overflow"} {
		t.Run(phase, func(t *testing.T) {
			mock := profileMockDB(t)
			begin := mock.ExpectBegin()
			if phase == "begin" {
				begin.WillReturnError(failure)
			} else {
				count := mock.ExpectQuery(regexp.QuoteMeta(messageCountSQL)).WithArgs(41)
				if phase == "count" {
					count.WillReturnError(failure)
				} else {
					total := int64(1)
					if phase == "total overflow" {
						total = int64(math.MaxInt32) + 1
					}
					count.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(total))
					find := mock.ExpectQuery(regexp.QuoteMeta(messagePageSQL)).WithArgs(41, 10)
					if phase == "find" {
						find.WillReturnError(failure)
					} else {
						find.WillReturnRows(sqlmock.NewRows([]string{"id"}))
					}
				}
				switch phase {
				case "commit":
					mock.ExpectCommit().WillReturnError(failure)
				case "total overflow":
					mock.ExpectCommit()
				default:
					mock.ExpectRollback()
				}
			}
			response, err := (&MessagesServer{}).ListMessages(context.Background(), &proto.MessageListRequest{UserId: 41})
			want := codes.Internal
			if phase == "total overflow" {
				want = codes.ResourceExhausted
			}
			if response != nil || status.Code(err) != want || (want == codes.Internal && status.Convert(err).Message() != "留言操作失败") {
				t.Fatalf("expected safe %s error, got %v / %v", want, response, err)
			}
		})
	}
}

func TestGetMessageScopesOwnerAndActiveRows(t *testing.T) {
	for _, outcome := range []string{"found", "missing or another owner or deleted", "read error"} {
		t.Run(outcome, func(t *testing.T) {
			mock := profileMockDB(t)
			created := time.Date(2026, 9, 25, 1, 2, 3, 0, time.UTC)
			query := mock.ExpectQuery(regexp.QuoteMeta(messageLookupSQL)).WithArgs(17, 41, 1)
			rows := sqlmock.NewRows([]string{"id", "user_id", "message_type", "subject", "message", "add_time"})
			want := codes.NotFound
			switch outcome {
			case "found":
				query.WillReturnRows(rows.AddRow(17, 41, 2, "主题", "内容", created))
				want = codes.OK
			case "read error":
				query.WillReturnError(errors.New("private database detail"))
				want = codes.Internal
			default:
				query.WillReturnRows(rows)
			}
			response, err := (&MessagesServer{}).GetMessage(context.Background(), &proto.MessageLookup{UserId: 41, Id: 17})
			if status.Code(err) != want {
				t.Fatalf("code=%s want=%s", status.Code(err), want)
			}
			if want == codes.OK {
				if response.Id != 17 || response.UserId != 41 || response.MessageType != 2 || response.Subject != "主题" || response.Message != "内容" || response.CreatedAt != created.Unix() {
					t.Fatalf("unexpected message: %v", response)
				}
			} else if response != nil || (want == codes.Internal && status.Convert(err).Message() != "留言操作失败") {
				t.Fatalf("read failure must not expose data or details: %v / %v", response, err)
			}
		})
	}
}

func TestDeleteMessageSoftDeletesOnlyOwnedActiveRowsAndChecksFailures(t *testing.T) {
	for _, outcome := range []string{"deleted", "missing or another owner or deleted", "begin error", "write error", "commit error"} {
		t.Run(outcome, func(t *testing.T) {
			mock := profileMockDB(t)
			failure := errors.New("private database detail")
			want := codes.Internal
			begin := mock.ExpectBegin()
			if outcome == "begin error" {
				begin.WillReturnError(failure)
			} else {
				update := mock.ExpectExec(regexp.QuoteMeta(messageDeleteSQL)).WithArgs(sqlmock.AnyArg(), 17, 41)
				if outcome == "write error" {
					update.WillReturnError(failure)
					mock.ExpectRollback()
				} else {
					affected := int64(1)
					if outcome == "missing or another owner or deleted" {
						affected = 0
						want = codes.NotFound
					} else if outcome == "deleted" {
						want = codes.OK
					}
					update.WillReturnResult(sqlmock.NewResult(0, affected))
					commit := mock.ExpectCommit()
					if outcome == "commit error" {
						commit.WillReturnError(failure)
					}
				}
			}
			response, err := (&MessagesServer{}).DeleteMessage(context.Background(), &proto.MessageLookup{UserId: 41, Id: 17})
			if status.Code(err) != want || (want == codes.OK && response == nil) || (want != codes.OK && response != nil) || (want == codes.Internal && status.Convert(err).Message() != "留言操作失败") {
				t.Fatalf("expected safe %s result, got %v / %v", want, response, err)
			}
		})
	}
}

func TestMessagesContextStopsAllMethodsBeforeDatabaseAccess(t *testing.T) {
	profileMockDB(t)
	server := &MessagesServer{}
	for _, expired := range []bool{false, true} {
		var ctx context.Context
		var cancel context.CancelFunc
		want := codes.Canceled
		if expired {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = codes.DeadlineExceeded
		} else {
			ctx, cancel = context.WithCancel(context.Background())
		}
		cancel()
		methods := []func() error{
			func() error { _, err := server.ListMessages(ctx, &proto.MessageListRequest{UserId: 41}); return err },
			func() error { _, err := server.GetMessage(ctx, &proto.MessageLookup{UserId: 41, Id: 17}); return err },
			func() error {
				_, err := server.CreateMessage(ctx, &proto.CreateMessageRequest{UserId: 41, MessageType: 1, Subject: "主题", Message: "内容"})
				return err
			},
			func() error {
				_, err := server.DeleteMessage(ctx, &proto.MessageLookup{UserId: 41, Id: 17})
				return err
			},
		}
		for _, method := range methods {
			if err := method(); status.Code(err) != want {
				t.Fatalf("context must propagate before database access: %v", err)
			}
		}
	}
}

func TestMessageDatabaseContextErrorsDoNotExposeWrappedDetails(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want codes.Code
	}{{context.Canceled, codes.Canceled}, {context.DeadlineExceeded, codes.DeadlineExceeded}} {
		t.Run(tc.want.String(), func(t *testing.T) {
			mock := profileMockDB(t)
			mock.ExpectQuery(regexp.QuoteMeta(messageLookupSQL)).WithArgs(17, 41, 1).WillReturnError(fmt.Errorf("private database detail: %w", tc.err))
			response, err := (&MessagesServer{}).GetMessage(context.Background(), &proto.MessageLookup{UserId: 41, Id: 17})
			if response != nil || status.Code(err) != tc.want || status.Convert(err).Message() != tc.err.Error() {
				t.Fatalf("wrapped context errors must retain their code without database details: %v / %v", response, err)
			}
		})
	}
}
