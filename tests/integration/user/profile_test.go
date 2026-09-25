//go:build integration

package user_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"grpctest/tests/integration/testdb"
	"grpctest/user_srv/global"
	"grpctest/user_srv/handler"
	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"

	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUserProfilePatch(t *testing.T) {
	previous := global.DB
	db := testdb.Open(t, "grpctest_user_srv")
	global.DB = db
	t.Cleanup(func() { global.DB = previous })
	ctx, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterUserServer(server, &handler.UserServer{}) })
	client := proto.NewUserClient(conn)
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("profile-fixture-only"), bcrypt.MinCost)
	if err != nil {
		t.Fatal("cannot prepare test password hash")
	}
	sequence := 0
	fixture := func(t *testing.T) model.User {
		t.Helper()
		sequence++
		birthday := time.Date(1998, time.May, 16, 0, 0, 0, 0, time.UTC)
		user := model.User{
			Mobile: fmt.Sprintf("141%08d", sequence), Password: string(passwordHash),
			NickName: "原昵称", Birthday: &birthday, Gender: "male", Role: 2,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal("cannot create isolated profile fixture")
		}
		t.Cleanup(func() { db.Unscoped().Delete(&model.User{}, user.ID) })
		// Read database precision back before comparing unchanged columns.
		if err := db.First(&user, user.ID).Error; err != nil {
			t.Fatal("cannot read isolated profile fixture")
		}
		return user
	}
	readDB := func(t *testing.T, id int32) model.User {
		t.Helper()
		var user model.User
		if err := db.Unscoped().First(&user, id).Error; err != nil {
			t.Fatal("cannot read profile row")
		}
		return user
	}
	assertProfile := func(t *testing.T, original model.User, nickname, gender, birthday string) {
		t.Helper()
		rsp, err := client.GetUserById(ctx, &proto.IdRequest{Id: original.ID})
		if err != nil {
			t.Fatalf("read profile RPC failed (gRPC code=%s)", status.Code(err))
		}
		if rsp.GetNickName() != nickname || rsp.GetGender() != gender {
			t.Fatal("profile fields differ from the requested partial update")
		}
		if rsp.GetBirthdayDate() != birthday {
			t.Fatalf("birthday date=%q, want %q", rsp.GetBirthdayDate(), birthday)
		}
		if rsp.GetMobile() != original.Mobile || rsp.GetPassword() != original.Password || rsp.GetRole() != int32(original.Role) {
			t.Fatal("profile read changed an unrelated account field")
		}
		if (rsp.BirthdayDate != nil) != (birthday != "") || (rsp.Birthday != nil) != (birthday != "") {
			t.Fatal("birthday presence was not preserved through protobuf transport")
		}
		actual := readDB(t, original.ID)
		if actual.Mobile != original.Mobile || actual.Password != original.Password || actual.Role != original.Role ||
			actual.IsDeleted != original.IsDeleted || !actual.CreatedAt.Equal(original.CreatedAt) || actual.DeletedAt != original.DeletedAt {
			t.Fatal("profile patch modified an unrelated account column")
		}
		if actual.NickName != nickname || actual.Gender != gender {
			t.Fatal("profile update was not persisted")
		}
		if birthday == "" {
			if actual.Birthday != nil {
				t.Fatal("explicit clear must persist SQL NULL")
			}
		} else if actual.Birthday == nil || actual.Birthday.Format("2006-01-02") != birthday {
			t.Fatal("birthday date was not persisted unchanged")
		}
	}
	patch := func(t *testing.T, request *proto.PatchUserInfo) {
		t.Helper()
		if _, err := client.PatchUser(ctx, request); err != nil {
			t.Fatalf("patch profile RPC failed (gRPC code=%s)", status.Code(err))
		}
	}

	t.Run("nickname only and identical retry preserve other fields", func(t *testing.T) {
		user := fixture(t)
		nickname := "新昵称"
		request := &proto.PatchUserInfo{Id: user.ID, NickName: &nickname}
		patch(t, request)
		assertProfile(t, user, nickname, "male", "1998-05-16")
		patch(t, request)
		assertProfile(t, user, nickname, "male", "1998-05-16")
	})

	t.Run("birthday set clear and omitted", func(t *testing.T) {
		user := fixture(t)
		for _, date := range []string{"1970-01-01", "1960-02-29", "1000-01-01", time.Now().UTC().Format("2006-01-02")} {
			patch(t, &proto.PatchUserInfo{Id: user.ID, BirthdayDate: &date})
			assertProfile(t, user, user.NickName, user.Gender, date)
		}
		clear := ""
		patch(t, &proto.PatchUserInfo{Id: user.ID, BirthdayDate: &clear})
		assertProfile(t, user, user.NickName, user.Gender, "")
		gender := "female"
		patch(t, &proto.PatchUserInfo{Id: user.ID, Gender: &gender})
		assertProfile(t, user, user.NickName, gender, "")
	})

	t.Run("concurrent disjoint fields do not overwrite each other", func(t *testing.T) {
		user := fixture(t)
		for i := 0; i < 8; i++ {
			nickname := fmt.Sprintf("concurrent-%d", i)
			gender := "female"
			if i%2 == 1 {
				gender = "male"
			}
			start, results := make(chan struct{}), make(chan error, 2)
			for _, request := range []*proto.PatchUserInfo{{Id: user.ID, NickName: &nickname}, {Id: user.ID, Gender: &gender}} {
				go func(request *proto.PatchUserInfo) {
					<-start
					_, err := client.PatchUser(ctx, request)
					results <- err
				}(request)
			}
			close(start)
			for j := 0; j < 2; j++ {
				if err := <-results; err != nil {
					t.Fatalf("concurrent patch failed (gRPC code=%s)", status.Code(err))
				}
			}
			assertProfile(t, user, nickname, gender, "1998-05-16")
		}
	})

	t.Run("invalid patches leave the entire row unchanged", func(t *testing.T) {
		user := fixture(t)
		empty, spaces, longName, invalidGender := "", "   ", strings.Repeat("名", 21), "other"
		cases := map[string]*proto.PatchUserInfo{
			"empty patch":      {Id: user.ID},
			"empty nickname":   {Id: user.ID, NickName: &empty},
			"blank nickname":   {Id: user.ID, NickName: &spaces},
			"long nickname":    {Id: user.ID, NickName: &longName},
			"empty gender":     {Id: user.ID, Gender: &empty},
			"invalid gender":   {Id: user.ID, Gender: &invalidGender},
			"zero user ID":     {Id: 0, NickName: &user.NickName},
			"negative user ID": {Id: -1, NickName: &user.NickName},
		}
		for _, date := range []string{"1998-5-16", "2025-02-29", "0999-12-31", "1998-05-16T00:00:00Z", time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")} {
			cases["invalid date "+date] = &proto.PatchUserInfo{Id: user.ID, BirthdayDate: &date}
		}
		newName, badDate := "should-not-persist", "2025-02-29"
		cases["invalid birthday cannot partially write nickname"] = &proto.PatchUserInfo{Id: user.ID, NickName: &newName, BirthdayDate: &badDate}
		for name, request := range cases {
			t.Run(name, func(t *testing.T) {
				_, err := client.PatchUser(ctx, request)
				if status.Code(err) != codes.InvalidArgument {
					t.Fatalf("invalid patch returned gRPC code=%s, want InvalidArgument", status.Code(err))
				}
				if actual := readDB(t, user.ID); !reflect.DeepEqual(actual, user) {
					t.Fatal("rejected patch changed the database row")
				}
			})
		}
	})

	t.Run("missing and soft deleted users remain unchanged", func(t *testing.T) {
		user := fixture(t)
		if err := db.Delete(&model.User{}, user.ID).Error; err != nil {
			t.Fatal("cannot soft delete profile fixture")
		}
		deleted := readDB(t, user.ID)
		nickname := "新昵称"
		for _, id := range []int32{2147483647, user.ID} {
			_, err := client.PatchUser(ctx, &proto.PatchUserInfo{Id: id, NickName: &nickname})
			if status.Code(err) != codes.NotFound {
				t.Fatalf("missing user returned gRPC code=%s, want NotFound", status.Code(err))
			}
		}
		if actual := readDB(t, user.ID); !reflect.DeepEqual(actual, deleted) {
			t.Fatal("patch revived or modified a soft deleted user")
		}
	})

	t.Run("legacy update respects presence and explicit epoch", func(t *testing.T) {
		user := fixture(t)
		nickname := "legacy-name"
		if _, err := client.Updateuser(ctx, &proto.UpdateUserInfo{Id: user.ID, NickName: &nickname}); err != nil {
			t.Fatalf("legacy partial update failed (gRPC code=%s)", status.Code(err))
		}
		assertProfile(t, user, nickname, "male", "1998-05-16")
		epoch := uint64(0)
		if _, err := client.Updateuser(ctx, &proto.UpdateUserInfo{Id: user.ID, BirthDay: &epoch}); err != nil {
			t.Fatalf("legacy explicit epoch update failed (gRPC code=%s)", status.Code(err))
		}
		assertProfile(t, user, nickname, "male", "1970-01-01")
	})
}
