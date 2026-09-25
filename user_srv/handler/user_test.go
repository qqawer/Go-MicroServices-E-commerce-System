package handler

import (
	"testing"
	"time"

	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"

	protobuf "google.golang.org/protobuf/proto"
)

func TestModelToResponseProfileOnWire(t *testing.T) {
	ordinary := time.Date(1998, time.May, 16, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	epoch := time.Unix(0, 0)
	beforeEpoch := time.Date(1960, time.June, 20, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		birthday *time.Time
	}{
		{"unset", nil},
		{"ordinary", &ordinary},
		{"unix_zero", &epoch},
		{"before_unix_epoch", &beforeEpoch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := model.User{
				BaseModel: model.BaseModel{ID: 41},
				Mobile:    "13000000001",
				Password:  "test-hash-placeholder",
				NickName:  "profile-user",
				Birthday:  tc.birthday,
				Gender:    "female",
				Role:      2,
			}
			wire, err := protobuf.Marshal(ModelToResponse(user))
			if err != nil {
				t.Fatal(err)
			}
			var got proto.UserInfoResponse
			if err := protobuf.Unmarshal(wire, &got); err != nil {
				t.Fatal(err)
			}
			if got.Id != user.ID || got.Mobile != user.Mobile || got.NickName != user.NickName || got.Gender != user.Gender || got.Role != int32(user.Role) {
				t.Fatal("profile fields were lost across the RPC boundary")
			}
			// Login still needs the hash internally; the Web DTO filters it out.
			if got.Password != user.Password {
				t.Fatal("password hash needed for login was lost")
			}
			if (got.Birthday != nil) != (tc.birthday != nil) {
				t.Fatal("birthday presence was lost across the RPC boundary")
			}
			if (got.BirthdayDate != nil) != (tc.birthday != nil) {
				t.Fatal("calendar birthday presence was lost across the RPC boundary")
			}
			if tc.birthday != nil && got.GetBirthdayDate() != tc.birthday.Format("2006-01-02") {
				t.Fatal("calendar birthday changed across the RPC boundary")
			}
			if tc.birthday != nil && int64(got.GetBirthday()) != tc.birthday.Unix() {
				t.Fatal("birthday timestamp changed across the RPC boundary")
			}
		})
	}
}
