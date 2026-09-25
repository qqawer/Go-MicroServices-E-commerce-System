//go:build integration

package user_test

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"grpctest/tests/integration/testdb"
	"grpctest/user_srv/global"
	"grpctest/user_srv/handler"
	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gproto "google.golang.org/protobuf/proto"
)

func TestMessages(t *testing.T) {
	previous := global.DB
	db := testdb.Open(t, "grpctest_user_srv")
	global.DB = db
	t.Cleanup(func() { global.DB = previous })
	service := &handler.MessagesServer{}
	_, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterMessagesServer(server, service) })
	client := proto.NewMessagesClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	requireCode := func(t *testing.T, err error, want codes.Code) {
		t.Helper()
		if status.Code(err) != want {
			t.Fatalf("message RPC code=%s, want %s", status.Code(err), want)
		}
	}
	owner := func(t *testing.T, role int) int32 {
		t.Helper()
		user := model.User{Mobile: "message-" + uuid.NewString(), NickName: "留言测试用户", Password: "test-only-message-fixture", Role: role}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal("cannot create isolated message owner")
		}
		t.Cleanup(func() {
			if err := db.Unscoped().Where("user_id = ?", user.ID).Delete(&model.UserMessage{}).Error; err != nil {
				t.Error("cannot remove isolated message rows")
			}
			if err := db.Unscoped().Delete(&model.User{}, user.ID).Error; err != nil {
				t.Error("cannot remove isolated message owner")
			}
		})
		return user.ID
	}
	request := func(userID int32) *proto.CreateMessageRequest {
		return &proto.CreateMessageRequest{UserId: userID, MessageType: 1, Subject: "配送咨询", Message: "请问商品何时可以送达？"}
	}
	create := func(t *testing.T, userID int32) *proto.UserMessage {
		t.Helper()
		message, err := client.CreateMessage(ctx, request(userID))
		requireCode(t, err, codes.OK)
		if message.GetId() <= 0 || message.GetUserId() != userID || message.GetCreatedAt() <= 0 {
			t.Fatal("created message must include a persisted ID, owner and creation time")
		}
		return message
	}
	readDB := func(t *testing.T, id int32) model.UserMessage {
		t.Helper()
		var message model.UserMessage
		if err := db.Unscoped().First(&message, id).Error; err != nil {
			t.Fatal("cannot inspect persisted message")
		}
		return message
	}
	count := func(t *testing.T, userID int32) int64 {
		t.Helper()
		var value int64
		if err := db.Unscoped().Model(&model.UserMessage{}).Where("user_id = ?", userID).Count(&value).Error; err != nil {
			t.Fatal("cannot count isolated message rows")
		}
		return value
	}

	t.Run("all message types and Unicode limits persist normalized content", func(t *testing.T) {
		userID := owner(t, 1)
		for messageType := int32(1); messageType <= 5; messageType++ {
			input := request(userID)
			input.MessageType = messageType
			input.Subject, input.Message = " \t配送咨询\u3000", "\n 请问商品何时可以送达？ \t"
			if messageType == 5 {
				input.Subject = " " + strings.Repeat("题", 99) + "📝 "
				input.Message = "\n" + strings.Repeat("文", 1999) + "📦\n"
			}
			created, err := client.CreateMessage(ctx, input)
			requireCode(t, err, codes.OK)
			if created.GetId() <= 0 || created.GetCreatedAt() <= 0 {
				t.Fatal("message response must contain its persisted ID and Unix creation time")
			}
			want := &proto.UserMessage{Id: created.Id, UserId: userID, MessageType: messageType,
				Subject: strings.TrimSpace(input.Subject), Message: strings.TrimSpace(input.Message), CreatedAt: created.CreatedAt}
			actual, err := client.GetMessage(ctx, &proto.MessageLookup{UserId: userID, Id: created.Id})
			requireCode(t, err, codes.OK)
			stored := readDB(t, created.Id)
			if !gproto.Equal(created, want) || !gproto.Equal(actual, want) || stored.UserID != userID ||
				int32(stored.MessageType) != messageType || stored.Subject != want.Subject || stored.Message != want.Message ||
				stored.CreatedAt.Unix() != want.CreatedAt || stored.DeletedAt.Valid {
				t.Fatal("normalized message content or creation time did not survive MySQL and protobuf transport")
			}
		}
	})

	t.Run("pagination has stable newest first order and excludes deleted rows and other owners", func(t *testing.T) {
		userA, userB := owner(t, 1), owner(t, 1)
		ids := make([]int32, 0, 12)
		for i := 0; i < 12; i++ {
			ids = append(ids, create(t, userA).Id)
		}
		other := create(t, userB)
		_, err := client.DeleteMessage(ctx, &proto.MessageLookup{UserId: userA, Id: ids[5]})
		requireCode(t, err, codes.OK)
		want := make([]int32, 0, 11)
		for i := len(ids) - 1; i >= 0; i-- {
			if i != 5 {
				want = append(want, ids[i])
			}
		}
		first, err := client.ListMessages(ctx, &proto.MessageListRequest{UserId: userA})
		requireCode(t, err, codes.OK)
		second, err := client.ListMessages(ctx, &proto.MessageListRequest{UserId: userA, Pn: 2, Psize: 10})
		requireCode(t, err, codes.OK)
		if first.Total != 11 || len(first.Data) != 10 || second.Total != 11 || len(second.Data) != 1 {
			t.Fatal("pagination must count only active messages for the owner and default to ten rows")
		}
		all := append(append([]*proto.UserMessage{}, first.Data...), second.Data...)
		for i, message := range all {
			if message.GetId() != want[i] || message.GetUserId() != userA {
				t.Fatal("message pagination changed descending ID order, repeated rows or leaked another owner")
			}
		}
		repeated, err := client.ListMessages(ctx, &proto.MessageListRequest{UserId: userA, Pn: 1, Psize: 10})
		requireCode(t, err, codes.OK)
		if !gproto.Equal(first, repeated) {
			t.Fatal("repeating the same page must preserve message order and contents")
		}
		maximum, err := client.ListMessages(ctx, &proto.MessageListRequest{UserId: userA, Psize: 100})
		requireCode(t, err, codes.OK)
		if maximum.Total != 11 || len(maximum.Data) != 11 {
			t.Fatal("maximum page size must return all active messages for this owner")
		}
		empty, err := client.ListMessages(ctx, &proto.MessageListRequest{UserId: userA, Pn: 3, Psize: 10})
		requireCode(t, err, codes.OK)
		if empty.Total != 11 || len(empty.Data) != 0 {
			t.Fatal("an empty page must preserve the owner's active message total")
		}
		otherPage, err := client.ListMessages(ctx, &proto.MessageListRequest{UserId: userB})
		requireCode(t, err, codes.OK)
		if otherPage.Total != 1 || len(otherPage.Data) != 1 || !gproto.Equal(otherPage.Data[0], other) {
			t.Fatal("listing another owner must return only that owner's message")
		}
		emptyOwner, err := client.ListMessages(ctx, &proto.MessageListRequest{UserId: owner(t, 1)})
		requireCode(t, err, codes.OK)
		if emptyOwner.Total != 0 || len(emptyOwner.Data) != 0 {
			t.Fatal("owner without messages must receive an empty list")
		}
	})

	t.Run("other users including administrators cannot read or delete another owner's message", func(t *testing.T) {
		original := create(t, owner(t, 1))
		before := readDB(t, original.Id)
		for _, userID := range []int32{owner(t, 1), owner(t, 2)} {
			lookup := &proto.MessageLookup{UserId: userID, Id: original.Id}
			_, err := client.GetMessage(ctx, lookup)
			requireCode(t, err, codes.NotFound)
			_, err = client.DeleteMessage(ctx, lookup)
			requireCode(t, err, codes.NotFound)
			page, err := client.ListMessages(ctx, &proto.MessageListRequest{UserId: userID})
			requireCode(t, err, codes.OK)
			if page.Total != 0 || len(page.Data) != 0 {
				t.Fatal("self message list leaked another owner's message")
			}
		}
		lookup := &proto.MessageLookup{UserId: original.UserId, Id: math.MaxInt32}
		_, err := client.GetMessage(ctx, lookup)
		requireCode(t, err, codes.NotFound)
		_, err = client.DeleteMessage(ctx, lookup)
		requireCode(t, err, codes.NotFound)
		if !reflect.DeepEqual(before, readDB(t, original.Id)) {
			t.Fatal("rejected cross-owner or unknown-ID request changed a stored message")
		}
	})

	t.Run("soft deletion retains content and repeated access cannot revive it", func(t *testing.T) {
		original := create(t, owner(t, 1))
		before := readDB(t, original.Id)
		lookup := &proto.MessageLookup{UserId: original.UserId, Id: original.Id}
		_, err := client.DeleteMessage(ctx, lookup)
		requireCode(t, err, codes.OK)
		deleted := readDB(t, original.Id)
		if !deleted.DeletedAt.Valid || deleted.Subject != before.Subject || deleted.Message != before.Message ||
			deleted.UserID != before.UserID || deleted.MessageType != before.MessageType || !deleted.CreatedAt.Equal(before.CreatedAt) {
			t.Fatal("soft deletion must preserve the persisted message and its original contents")
		}
		_, err = client.GetMessage(ctx, lookup)
		requireCode(t, err, codes.NotFound)
		_, err = client.DeleteMessage(ctx, lookup)
		requireCode(t, err, codes.NotFound)
		page, err := client.ListMessages(ctx, &proto.MessageListRequest{UserId: original.UserId})
		requireCode(t, err, codes.OK)
		if page.Total != 0 || len(page.Data) != 0 || !reflect.DeepEqual(deleted, readDB(t, original.Id)) {
			t.Fatal("deleted message was listed, modified or revived")
		}
	})

	t.Run("invalid creates and inactive owners never persist", func(t *testing.T) {
		userID := owner(t, 1)
		invalid := map[string]func(*proto.CreateMessageRequest){
			"zero owner":       func(r *proto.CreateMessageRequest) { r.UserId = 0 },
			"negative owner":   func(r *proto.CreateMessageRequest) { r.UserId = -1 },
			"zero type":        func(r *proto.CreateMessageRequest) { r.MessageType = 0 },
			"negative type":    func(r *proto.CreateMessageRequest) { r.MessageType = -1 },
			"unsupported type": func(r *proto.CreateMessageRequest) { r.MessageType = 6 },
			"empty subject":    func(r *proto.CreateMessageRequest) { r.Subject = "" },
			"blank subject":    func(r *proto.CreateMessageRequest) { r.Subject = " \t\u3000" },
			"long subject":     func(r *proto.CreateMessageRequest) { r.Subject = strings.Repeat("题", 101) },
			"empty message":    func(r *proto.CreateMessageRequest) { r.Message = "" },
			"blank message":    func(r *proto.CreateMessageRequest) { r.Message = "\n \t\u3000" },
			"long message":     func(r *proto.CreateMessageRequest) { r.Message = strings.Repeat("文", 2001) },
		}
		for name, mutate := range invalid {
			t.Run(name, func(t *testing.T) {
				input := request(userID)
				mutate(input)
				_, err := client.CreateMessage(ctx, input)
				requireCode(t, err, codes.InvalidArgument)
			})
		}
		// Protobuf transport rejects invalid UTF-8 before the handler, so exercise
		// handler validation directly while retaining the same real database.
		for _, field := range []string{"subject", "message"} {
			input := request(userID)
			if field == "subject" {
				input.Subject = string([]byte{0xff})
			} else {
				input.Message = string([]byte{0xff})
			}
			_, err := service.CreateMessage(ctx, input)
			requireCode(t, err, codes.InvalidArgument)
		}
		deletedOwner := owner(t, 1)
		if err := db.Delete(&model.User{}, deletedOwner).Error; err != nil {
			t.Fatal("cannot prepare inactive message owner")
		}
		for _, id := range []int32{deletedOwner, math.MaxInt32} {
			_, err := client.CreateMessage(ctx, request(id))
			requireCode(t, err, codes.NotFound)
		}
		for _, id := range []int32{userID, 0, -1, deletedOwner, math.MaxInt32} {
			if count(t, id) != 0 {
				t.Fatal("invalid message or inactive owner persisted a row")
			}
		}
	})

	t.Run("nonpositive keys and invalid pagination leave existing messages unchanged", func(t *testing.T) {
		original := create(t, owner(t, 1))
		before := readDB(t, original.Id)
		for _, id := range []int32{0, -1} {
			_, err := client.ListMessages(ctx, &proto.MessageListRequest{UserId: id})
			requireCode(t, err, codes.InvalidArgument)
			for _, lookup := range []*proto.MessageLookup{{UserId: id, Id: original.Id}, {UserId: original.UserId, Id: id}} {
				_, err := client.GetMessage(ctx, lookup)
				requireCode(t, err, codes.InvalidArgument)
				_, err = client.DeleteMessage(ctx, lookup)
				requireCode(t, err, codes.InvalidArgument)
			}
		}
		for _, page := range [][2]int32{{-1, 10}, {1, -1}, {1, 101}, {math.MaxInt32, 100}} {
			_, err := client.ListMessages(ctx, &proto.MessageListRequest{UserId: original.UserId, Pn: page[0], Psize: page[1]})
			requireCode(t, err, codes.InvalidArgument)
		}
		page, err := client.ListMessages(ctx, &proto.MessageListRequest{UserId: original.UserId, Pn: math.MaxInt32, Psize: 1})
		requireCode(t, err, codes.OK)
		if page.Total != 1 || len(page.Data) != 0 {
			t.Fatal("a representable maximum offset must return an empty page with the full total")
		}
		if !reflect.DeepEqual(before, readDB(t, original.Id)) {
			t.Fatal("invalid message keys or pagination changed an existing row")
		}
	})

	t.Run("nil requests are rejected by every handler", func(t *testing.T) {
		_, err := service.ListMessages(ctx, nil)
		requireCode(t, err, codes.InvalidArgument)
		_, err = service.GetMessage(ctx, nil)
		requireCode(t, err, codes.InvalidArgument)
		_, err = service.CreateMessage(ctx, nil)
		requireCode(t, err, codes.InvalidArgument)
		_, err = service.DeleteMessage(ctx, nil)
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("cancellation and elapsed deadlines reach handlers without writing", func(t *testing.T) {
		original := create(t, owner(t, 1))
		before := readDB(t, original.Id)
		lookup := &proto.MessageLookup{UserId: original.UserId, Id: original.Id}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		expired, finish := context.WithDeadline(ctx, time.Now().Add(-time.Second))
		defer finish()
		for _, test := range []struct {
			ctx  context.Context
			code codes.Code
		}{{canceled, codes.Canceled}, {expired, codes.DeadlineExceeded}} {
			// Direct calls ensure database work honors the context rather than
			// testing only gRPC's client-side cancellation behavior.
			_, err := service.ListMessages(test.ctx, &proto.MessageListRequest{UserId: original.UserId})
			requireCode(t, err, test.code)
			_, err = service.GetMessage(test.ctx, lookup)
			requireCode(t, err, test.code)
			_, err = service.CreateMessage(test.ctx, request(original.UserId))
			requireCode(t, err, test.code)
			_, err = service.DeleteMessage(test.ctx, lookup)
			requireCode(t, err, test.code)
		}
		if count(t, original.UserId) != 1 || !reflect.DeepEqual(before, readDB(t, original.Id)) {
			t.Fatal("canceled message request wrote or deleted a row")
		}
	})

	t.Run("database failures return Internal without changing existing messages", func(t *testing.T) {
		original := create(t, owner(t, 1))
		before := readDB(t, original.Id)
		// A separate verified test connection can be closed without interrupting
		// fixture cleanup, changing schema or touching another test's pool.
		failedDB := testdb.Open(t, "grpctest_user_srv")
		pool, err := failedDB.DB()
		if err != nil || pool.Close() != nil {
			t.Fatal("cannot prepare closed integration database connection")
		}
		global.DB = failedDB
		defer func() { global.DB = db }()
		lookup := &proto.MessageLookup{UserId: original.UserId, Id: original.Id}
		_, err = client.ListMessages(ctx, &proto.MessageListRequest{UserId: original.UserId})
		requireCode(t, err, codes.Internal)
		_, err = client.GetMessage(ctx, lookup)
		requireCode(t, err, codes.Internal)
		_, err = client.CreateMessage(ctx, request(original.UserId))
		requireCode(t, err, codes.Internal)
		_, err = client.DeleteMessage(ctx, lookup)
		requireCode(t, err, codes.Internal)
		if count(t, original.UserId) != 1 || !reflect.DeepEqual(before, readDB(t, original.Id)) {
			t.Fatal("failed message operations changed the persisted data")
		}
	})
}
