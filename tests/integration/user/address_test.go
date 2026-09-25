//go:build integration

package user_test

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"grpctest/tests/integration/testdb"
	"grpctest/user_srv/global"
	"grpctest/user_srv/handler"
	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gproto "google.golang.org/protobuf/proto"
)

func TestAddressBook(t *testing.T) {
	previous := global.DB
	db := testdb.Open(t, "grpctest_user_srv")
	global.DB = db
	t.Cleanup(func() { global.DB = previous })
	ctx, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterAddressBookServer(server, &handler.AddressServer{}) })
	client := proto.NewAddressBookClient(conn)
	requireCode := func(t *testing.T, err error, want codes.Code) {
		t.Helper()
		if status.Code(err) != want {
			t.Fatalf("address RPC returned code=%s, want %s", status.Code(err), want)
		}
	}
	sequence := 0
	owner := func(t *testing.T) int32 {
		t.Helper()
		sequence++
		user := model.User{Mobile: fmt.Sprintf("142%08d", sequence), NickName: "地址测试用户", Password: "test-only-address-fixture"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal("cannot create isolated address owner")
		}
		t.Cleanup(func() {
			db.Unscoped().Where("user_id = ?", user.ID).Delete(&model.Address{})
			db.Unscoped().Delete(&model.User{}, user.ID)
		})
		return user.ID
	}
	request := func(userID int32) *proto.CreateAddressRequest {
		return &proto.CreateAddressRequest{
			UserId: userID, Name: "小王", Mobile: "13800138000",
			Province: "浙江省", City: "杭州市", District: "西湖区", Address: "文三路一号",
		}
	}
	create := func(t *testing.T, userID int32) *proto.Address {
		t.Helper()
		address, err := client.CreateAddress(ctx, request(userID))
		requireCode(t, err, codes.OK)
		if address.GetId() <= 0 || address.GetUserId() != userID {
			t.Fatal("created address must carry a persisted ID and its owner")
		}
		return address
	}
	read := func(t *testing.T, address *proto.Address) *proto.Address {
		t.Helper()
		actual, err := client.GetAddress(ctx, &proto.AddressLookup{UserId: address.UserId, Id: address.Id})
		requireCode(t, err, codes.OK)
		return actual
	}
	readDB := func(t *testing.T, id int32) model.Address {
		t.Helper()
		var address model.Address
		if err := db.Unscoped().First(&address, id).Error; err != nil {
			t.Fatal("cannot read persisted address")
		}
		return address
	}

	t.Run("create trims text and persists every field", func(t *testing.T) {
		userID := owner(t)
		input := request(userID)
		input.Name, input.Mobile = "  小王  ", " 13800138000 "
		input.Province, input.City = " 浙江省 ", " 杭州市 "
		input.District, input.Address = " 西湖区 ", " 文三路一号 "
		created, err := client.CreateAddress(ctx, input)
		requireCode(t, err, codes.OK)
		want := &proto.Address{Id: created.Id, UserId: userID, Name: "小王", Mobile: "13800138000", Province: "浙江省", City: "杭州市", District: "西湖区", Address: "文三路一号"}
		if created.Id <= 0 || !gproto.Equal(created, want) || !gproto.Equal(read(t, created), want) {
			t.Fatal("normalized address was not returned and persisted through gRPC")
		}
	})

	t.Run("pagination stays within one owner and retains the full total", func(t *testing.T) {
		userA, userB := owner(t), owner(t)
		ids := map[int32]bool{}
		for i := 0; i < 3; i++ {
			ids[create(t, userA).Id] = true
		}
		other := create(t, userB)
		first, err := client.ListAddresses(ctx, &proto.AddressListRequest{UserId: userA, Pn: 1, Psize: 2})
		requireCode(t, err, codes.OK)
		second, err := client.ListAddresses(ctx, &proto.AddressListRequest{UserId: userA, Pn: 2, Psize: 2})
		requireCode(t, err, codes.OK)
		if first.Total != 3 || second.Total != 3 || len(first.Data) != 2 || len(second.Data) != 1 {
			t.Fatal("pagination must retain the owner's full total and requested page size")
		}
		seen := map[int32]bool{}
		for _, address := range append(first.Data, second.Data...) {
			if !ids[address.Id] || address.UserId != userA || seen[address.Id] {
				t.Fatal("pagination leaked another owner or repeated an address")
			}
			seen[address.Id] = true
		}
		repeated, err := client.ListAddresses(ctx, &proto.AddressListRequest{UserId: userA, Pn: 1, Psize: 2})
		requireCode(t, err, codes.OK)
		if !gproto.Equal(first, repeated) {
			t.Fatal("repeated pagination must have stable ordering")
		}
		defaults, err := client.ListAddresses(ctx, &proto.AddressListRequest{UserId: userB})
		requireCode(t, err, codes.OK)
		if defaults.Total != 1 || len(defaults.Data) != 1 || !gproto.Equal(defaults.Data[0], other) {
			t.Fatal("default pagination must return only the current owner's addresses")
		}
		empty, err := client.ListAddresses(ctx, &proto.AddressListRequest{UserId: userA, Pn: 3, Psize: 2})
		requireCode(t, err, codes.OK)
		if empty.Total != 3 || len(empty.Data) != 0 {
			t.Fatal("an empty page must preserve the owner's total")
		}
	})

	t.Run("partial update and identical retry preserve omitted fields", func(t *testing.T) {
		original := create(t, owner(t))
		name := " 小李 "
		patch := &proto.UpdateAddressRequest{UserId: original.UserId, Id: original.Id, Name: &name}
		want := gproto.Clone(original).(*proto.Address)
		want.Name = "小李"
		for i := 0; i < 2; i++ {
			updated, err := client.UpdateAddress(ctx, patch)
			requireCode(t, err, codes.OK)
			if !gproto.Equal(updated, want) || !gproto.Equal(read(t, original), want) {
				t.Fatal("partial update must retain all omitted fields, including on an identical retry")
			}
		}
	})

	t.Run("concurrent changes to different fields are both retained", func(t *testing.T) {
		original := create(t, owner(t))
		for i := 0; i < 8; i++ {
			name, detail := fmt.Sprintf("收货人%d", i), fmt.Sprintf("文三路%d号", i)
			start, results := make(chan struct{}), make(chan error, 2)
			for _, patch := range []*proto.UpdateAddressRequest{
				{UserId: original.UserId, Id: original.Id, Name: &name},
				{UserId: original.UserId, Id: original.Id, Address: &detail},
			} {
				go func(patch *proto.UpdateAddressRequest) {
					<-start
					_, err := client.UpdateAddress(ctx, patch)
					results <- err
				}(patch)
			}
			close(start)
			for j := 0; j < 2; j++ {
				requireCode(t, <-results, codes.OK)
			}
			want := gproto.Clone(original).(*proto.Address)
			want.Name, want.Address = name, detail
			if !gproto.Equal(read(t, original), want) {
				t.Fatal("concurrent partial updates lost one update or changed an omitted field")
			}
		}
	})

	t.Run("other users and unknown IDs cannot read update or delete", func(t *testing.T) {
		original, userB := create(t, owner(t)), owner(t)
		before := readDB(t, original.Id)
		name := "越权修改"
		for _, lookup := range []*proto.AddressLookup{{UserId: userB, Id: original.Id}, {UserId: original.UserId, Id: math.MaxInt32}} {
			_, err := client.GetAddress(ctx, lookup)
			requireCode(t, err, codes.NotFound)
			_, err = client.UpdateAddress(ctx, &proto.UpdateAddressRequest{UserId: lookup.UserId, Id: lookup.Id, Name: &name})
			requireCode(t, err, codes.NotFound)
			_, err = client.DeleteAddress(ctx, lookup)
			requireCode(t, err, codes.NotFound)
		}
		if after := readDB(t, original.Id); !reflect.DeepEqual(before, after) {
			t.Fatal("cross-user requests changed an address")
		}
	})

	t.Run("soft delete excludes the address and cannot be revived", func(t *testing.T) {
		original := create(t, owner(t))
		lookup := &proto.AddressLookup{UserId: original.UserId, Id: original.Id}
		_, err := client.DeleteAddress(ctx, lookup)
		requireCode(t, err, codes.OK)
		deleted := readDB(t, original.Id)
		if !deleted.DeletedAt.Valid {
			t.Fatal("address deletion must preserve a soft-deleted row")
		}
		_, err = client.GetAddress(ctx, lookup)
		requireCode(t, err, codes.NotFound)
		_, err = client.UpdateAddress(ctx, &proto.UpdateAddressRequest{UserId: original.UserId, Id: original.Id, Name: &original.Name})
		requireCode(t, err, codes.NotFound)
		_, err = client.DeleteAddress(ctx, lookup)
		requireCode(t, err, codes.NotFound)
		list, err := client.ListAddresses(ctx, &proto.AddressListRequest{UserId: original.UserId})
		requireCode(t, err, codes.OK)
		if list.Total != 0 || len(list.Data) != 0 || !reflect.DeepEqual(readDB(t, original.Id), deleted) {
			t.Fatal("deleted address was listed, modified or revived")
		}
	})

	t.Run("invalid creates and missing owners never persist", func(t *testing.T) {
		userID := owner(t)
		for _, mutate := range []func(*proto.CreateAddressRequest){
			func(r *proto.CreateAddressRequest) { r.UserId = 0 },
			func(r *proto.CreateAddressRequest) { r.UserId = -1 },
			func(r *proto.CreateAddressRequest) { r.Name = " " },
			func(r *proto.CreateAddressRequest) { r.Name = strings.Repeat("名", 21) },
			func(r *proto.CreateAddressRequest) { r.Mobile = "23800138000" },
			func(r *proto.CreateAddressRequest) { r.Mobile = "1380013800" },
			func(r *proto.CreateAddressRequest) { r.Mobile = "1380013800a" },
			func(r *proto.CreateAddressRequest) { r.Province = "" },
			func(r *proto.CreateAddressRequest) { r.City = strings.Repeat("市", 21) },
			func(r *proto.CreateAddressRequest) { r.District = " " },
			func(r *proto.CreateAddressRequest) { r.Address = "" },
			func(r *proto.CreateAddressRequest) { r.Address = strings.Repeat("址", 101) },
			func(r *proto.CreateAddressRequest) { r.Address = strings.Repeat("址", 100) },
		} {
			input := request(userID)
			mutate(input)
			_, err := client.CreateAddress(ctx, input)
			requireCode(t, err, codes.InvalidArgument)
		}
		deletedOwner := owner(t)
		if err := db.Delete(&model.User{}, deletedOwner).Error; err != nil {
			t.Fatal("cannot prepare deleted address owner")
		}
		for _, id := range []int32{math.MaxInt32, deletedOwner} {
			_, err := client.CreateAddress(ctx, request(id))
			requireCode(t, err, codes.NotFound)
		}
		var count int64
		if err := db.Unscoped().Model(&model.Address{}).Where("user_id IN ?", []int32{0, -1, userID, deletedOwner, math.MaxInt32}).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("invalid address creation persisted a row")
		}
	})

	t.Run("invalid patches leave the complete row unchanged", func(t *testing.T) {
		original := create(t, owner(t))
		before := readDB(t, original.Id)
		empty, blank, longName, badMobile := "", "   ", strings.Repeat("名", 21), "invalid"
		tooLong, changed := strings.Repeat("址", 100), "不能部分保存"
		for _, patch := range []*proto.UpdateAddressRequest{
			{UserId: original.UserId, Id: original.Id},
			{UserId: original.UserId, Id: original.Id, Name: &empty},
			{UserId: original.UserId, Id: original.Id, Name: &longName},
			{UserId: original.UserId, Id: original.Id, Mobile: &badMobile},
			{UserId: original.UserId, Id: original.Id, Province: &blank},
			{UserId: original.UserId, Id: original.Id, City: &empty},
			{UserId: original.UserId, Id: original.Id, District: &blank},
			{UserId: original.UserId, Id: original.Id, Address: &empty},
			{UserId: original.UserId, Id: original.Id, Name: &changed, Address: &tooLong},
		} {
			_, err := client.UpdateAddress(ctx, patch)
			requireCode(t, err, codes.InvalidArgument)
			if after := readDB(t, original.Id); !reflect.DeepEqual(before, after) {
				t.Fatal("invalid patch partially updated the address")
			}
		}
	})

	t.Run("combined address length is validated after merging partial changes", func(t *testing.T) {
		input := request(owner(t))
		input.Province, input.City, input.District = strings.Repeat("省", 20), strings.Repeat("市", 20), strings.Repeat("区", 20)
		input.Address = strings.Repeat("址", 37) // Three separating spaces bring the order snapshot to exactly 100 runes.
		original, err := client.CreateAddress(ctx, input)
		requireCode(t, err, codes.OK)
		before := readDB(t, original.Id)
		longer := strings.Repeat("址", 38)
		_, err = client.UpdateAddress(ctx, &proto.UpdateAddressRequest{UserId: original.UserId, Id: original.Id, Address: &longer})
		requireCode(t, err, codes.InvalidArgument)
		if !reflect.DeepEqual(readDB(t, original.Id), before) {
			t.Fatal("partial update exceeded the merged order address length")
		}
	})

	t.Run("nonpositive IDs and invalid page bounds are rejected", func(t *testing.T) {
		original := create(t, owner(t))
		for _, id := range []int32{0, -1} {
			_, err := client.ListAddresses(ctx, &proto.AddressListRequest{UserId: id})
			requireCode(t, err, codes.InvalidArgument)
			for _, lookup := range []*proto.AddressLookup{{UserId: id, Id: original.Id}, {UserId: original.UserId, Id: id}} {
				_, err = client.GetAddress(ctx, lookup)
				requireCode(t, err, codes.InvalidArgument)
				_, err = client.UpdateAddress(ctx, &proto.UpdateAddressRequest{UserId: lookup.UserId, Id: lookup.Id, Name: &original.Name})
				requireCode(t, err, codes.InvalidArgument)
				_, err = client.DeleteAddress(ctx, lookup)
				requireCode(t, err, codes.InvalidArgument)
			}
		}
		for _, pagination := range [][2]int32{{-1, 10}, {1, -1}, {1, 101}, {math.MaxInt32, 100}} {
			_, err := client.ListAddresses(ctx, &proto.AddressListRequest{UserId: original.UserId, Pn: pagination[0], Psize: pagination[1]})
			requireCode(t, err, codes.InvalidArgument)
		}
		page, err := client.ListAddresses(ctx, &proto.AddressListRequest{UserId: original.UserId, Pn: 1, Psize: 100})
		requireCode(t, err, codes.OK)
		if page.Total != 1 || len(page.Data) != 1 {
			t.Fatal("maximum supported page size must remain valid")
		}
	})
}
