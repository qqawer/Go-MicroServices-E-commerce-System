package handler

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "google.golang.org/protobuf/proto"
	"grpctest/order_srv/global"
	"grpctest/order_srv/proto"
)

type addressLookupClient struct {
	proto.AddressBookClient
	lookup func(context.Context, *proto.AddressLookup) (*proto.Address, error)
}

func (f *addressLookupClient) GetAddress(ctx context.Context, req *proto.AddressLookup, _ ...grpc.CallOption) (*proto.Address, error) {
	return f.lookup(ctx, req)
}

func useAddressClient(t *testing.T, client proto.AddressBookClient) {
	t.Helper()
	old := global.AddressBookClient
	global.AddressBookClient = client
	t.Cleanup(func() { global.AddressBookClient = old })
}

func selectedAddressRequest() *proto.OrderRequest {
	return &proto.OrderRequest{UserId: 41, AddressId: 7, RequestKey: "selected-address", Post: "测试"}
}

func TestOrderCreationHashPreservesLegacyAndSelectorIdentity(t *testing.T) {
	legacy := &proto.OrderRequest{UserId: 41, Name: "测试", Address: "测试地址", Mobile: "13000000001", Post: "测试"}
	const beforeAddressSupport = "c291d8196fc142eeca1ce3b6060cb9de7fcf45d108bd5cf9724f85072aa4c161"
	if got := requestHash(legacy); got != beforeAddressSupport {
		t.Fatal("previously accepted direct-address orders would no longer replay", got)
	}
	selected := selectedAddressRequest()
	base := requestHash(selected)
	if base == beforeAddressSupport {
		t.Fatal("selector and direct-address modes must not share request identity")
	}
	for _, change := range []func(*proto.OrderRequest){
		func(r *proto.OrderRequest) { r.UserId++ },
		func(r *proto.OrderRequest) { r.AddressId++ },
		func(r *proto.OrderRequest) { r.Post = "不同留言" },
	} {
		changed := pb.Clone(selected).(*proto.OrderRequest)
		change(changed)
		if requestHash(changed) == base {
			t.Fatal("changed selector reused request identity")
		}
	}
}

func TestOrderCreationMethodsRejectMixedOrMissingShippingInputs(t *testing.T) {
	if err := validateAddressCreation(selectedAddressRequest()); err != nil {
		t.Fatal(err)
	}
	for _, req := range []*proto.OrderRequest{
		nil, {}, {UserId: 41, RequestKey: "key"},
		{UserId: 41, RequestKey: "key", AddressId: -1},
		{UserId: 41, RequestKey: "bad key", AddressId: 7},
		{UserId: 0, RequestKey: "key", AddressId: 7},
	} {
		if status.Code(validateAddressCreation(req)) != codes.InvalidArgument {
			t.Fatalf("invalid selector accepted: %v", req)
		}
	}
	for _, change := range []func(*proto.OrderRequest){
		func(r *proto.OrderRequest) { r.Name = "混入姓名" },
		func(r *proto.OrderRequest) { r.Mobile = "13000000001" },
		func(r *proto.OrderRequest) { r.Address = "混入地址" },
		func(r *proto.OrderRequest) { r.Id = 1 },
		func(r *proto.OrderRequest) { r.Post = strings.Repeat("字", 21) },
	} {
		r := selectedAddressRequest()
		change(r)
		if status.Code(validateAddressCreation(r)) != codes.InvalidArgument {
			t.Fatalf("mixed selector accepted: %v", r)
		}
	}
	legacy := &proto.OrderRequest{UserId: 41, RequestKey: "key", Name: "测试", Address: "测试地址", Mobile: "13000000001"}
	for _, id := range []int32{-1, 7} {
		legacy.AddressId = id
		if status.Code(validateCreation(legacy)) != codes.InvalidArgument {
			t.Fatal("legacy method ignored address selector")
		}
	}
}

func TestAddressShippingChecksOwnershipAndPreservesOriginalRequest(t *testing.T) {
	request := selectedAddressRequest()
	original := pb.Clone(request)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	address := &proto.Address{Id: 7, UserId: 41, Name: "测试", Mobile: "13000000001", Province: "省", City: "市", District: "区", Address: "原地址"}
	useAddressClient(t, &addressLookupClient{lookup: func(got context.Context, lookup *proto.AddressLookup) (*proto.Address, error) {
		if got != ctx || lookup.UserId != 41 || lookup.Id != 7 {
			t.Fatal("address lookup lost request context or owner", lookup)
		}
		return address, nil
	}})
	shipping, err := creationShipping(ctx, request)
	if err != nil || shipping.Address != "省 市 区 原地址" || shipping.SignerName != "测试" || shipping.SingerMobile != "13000000001" {
		t.Fatal("wrong shipping snapshot", shipping, err)
	}
	if !pb.Equal(request, original) {
		t.Fatal("address resolution changed the original request/hash")
	}
	address.UserId = 42
	if _, err := creationShipping(ctx, request); status.Code(err) != codes.NotFound {
		t.Fatal("cross-user response was accepted", err)
	}
	address.UserId, address.Id = 41, 8
	if _, err := creationShipping(ctx, request); status.Code(err) != codes.NotFound {
		t.Fatal("wrong address ID was accepted", err)
	}
}

func TestAddressShippingRejectsOversizedCombinedAddress(t *testing.T) {
	address := &proto.Address{Id: 7, UserId: 41, Name: "测试", Mobile: "13000000001", Province: "省", City: "市", District: "区", Address: strings.Repeat("界", 94)}
	useAddressClient(t, &addressLookupClient{lookup: func(context.Context, *proto.AddressLookup) (*proto.Address, error) { return address, nil }})
	if _, err := creationShipping(context.Background(), selectedAddressRequest()); err != nil {
		t.Fatal("100-rune shipping address must fit the existing order column", err)
	}
	address.Address += "界"
	if _, err := creationShipping(context.Background(), selectedAddressRequest()); status.Code(err) != codes.InvalidArgument {
		t.Fatal("combined shipping address exceeded the database column", err)
	}
	address.Address = " "
	if _, err := creationShipping(context.Background(), selectedAddressRequest()); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("blank shipping component accepted", err)
	}
}

func TestExistingAddressIntentReplaysWithoutAnyAddressLookup(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same request", true: "conflicting request"}[changed], func(t *testing.T) {
			mock := cartMock(t)
			useAddressClient(t, nil)
			req := selectedAddressRequest()
			hash := requestHash(req)
			if changed {
				req.AddressId++
			}
			mock.ExpectQuery("SELECT .* FROM `order_transaction`").WithArgs(req.UserId, req.RequestKey, 1).
				WillReturnRows(sqlmock.NewRows([]string{"order_sn", "user_id", "request_key", "request_hash", "snapshot"}).AddRow("fixed-order", 41, req.RequestKey, hash, `{"Order":{"Address":"旧地址"}}`))
			record, err := prepareCreation(context.Background(), req)
			if changed {
				if status.Code(err) != codes.AlreadyExists {
					t.Fatal("changed selector must conflict before address lookup", err)
				}
			} else if err != nil || record.OrderSn != "fixed-order" || record.Snapshot == nil || *record.Snapshot != `{"Order":{"Address":"旧地址"}}` {
				t.Fatal("existing snapshot was not replayed independently of the address service", record, err)
			}
		})
	}
}

func TestAddressDependencyOnlyAppliesToNewSelectedAddressIntent(t *testing.T) {
	mock := cartMock(t)
	useAddressClient(t, nil)
	req := selectedAddressRequest()
	mock.ExpectQuery("SELECT .* FROM `order_transaction`").WillReturnRows(sqlmock.NewRows([]string{"order_sn"}))
	if _, err := prepareCreation(context.Background(), req); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("new selected-address intent must require address service", err)
	}
	legacy := &proto.OrderRequest{UserId: 41, Name: "测试", Address: "直接地址", Mobile: "13000000001"}
	if shipping, err := creationShipping(context.Background(), legacy); err != nil || shipping.Address != legacy.Address {
		t.Fatal("direct-address checkout acquired an address service dependency", err)
	}
}
