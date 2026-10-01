//go:build integration

package goods_test

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"
)

func TestCategoryUpdateRejectsInvalidHierarchy(t *testing.T) {
	for _, scenario := range []string{
		"zero ID", "negative ID", "missing ID", "level too high", "negative level",
		"negative parent", "self parent", "missing parent", "deleted parent", "wrong parent level",
		"root with parent", "child promoted with retained parent", "root without required parent",
		"parent level change", "move below descendant",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx, client := categoryClient(t)
			create := func(level, parent int32) int32 {
				r, err := client.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Hierarchy fixture", Level: level, ParentCategory: parent, IsTab: true})
				if err != nil {
					t.Fatal(err)
				}
				return r.Id
			}
			root := create(1, 0)
			child := create(2, root)
			leaf := create(3, child)
			other := create(1, 0)
			req := &proto.CategoryInfoRequest{Id: child, Name: "Must not persist", IsTab: false}
			want := codes.InvalidArgument
			switch scenario {
			case "zero ID":
				req.Id = 0
			case "negative ID":
				req.Id = -1
			case "missing ID":
				req.Id = 2147483647
				want = codes.NotFound
			case "level too high":
				req.Level = 4
			case "negative level":
				req.Level = -1
			case "negative parent":
				req.ParentCategory = -1
			case "self parent":
				req.ParentCategory = child
			case "missing parent":
				req.ParentCategory = 2147483647
			case "deleted parent":
				if _, err := client.DeleteCategory(ctx, &proto.DeleteCategoryRequest{Id: other}); err != nil {
					t.Fatal(err)
				}
				req.ParentCategory = other
			case "wrong parent level":
				req.Id = leaf
				req.ParentCategory = other
			case "root with parent":
				req.Id = root
				req.ParentCategory = other
			case "child promoted with retained parent":
				req.Level = 1
			case "root without required parent":
				req.Id = other
				req.Level = 2
			case "parent level change":
				req.Id = root
				req.Level = 2
				req.ParentCategory = other
				want = codes.FailedPrecondition
			case "move below descendant":
				req.Id = root
				req.Level = 3
				req.ParentCategory = child
				want = codes.FailedPrecondition
			}
			var before, after []model.Category
			if err := global.DB.Unscoped().Order("id").Find(&before).Error; err != nil {
				t.Fatal(err)
			}
			_, err := client.UpdateCategory(ctx, req)
			if status.Code(err) != want {
				t.Fatalf("got %v, want %s", err, want)
			}
			if err := global.DB.Unscoped().Order("id").Find(&after).Error; err != nil {
				t.Fatal(err)
			}
			if len(before) != len(after) {
				t.Fatal("rejection changed category count")
			}
			for i := range before {
				b, a := before[i], after[i]
				if b.ID != a.ID || b.Name != a.Name || b.Level != a.Level || b.ParentCategoryID != a.ParentCategoryID || b.IsTab != a.IsTab || !b.UpdatedAt.Equal(a.UpdatedAt) {
					t.Fatalf("rejected update changed category %d", b.ID)
				}
			}
		})
	}
}

func TestCategoryUpdateAllowsValidMoves(t *testing.T) {
	ctx, client := categoryClient(t)
	create := func(level, parent int32) int32 {
		r, err := client.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Hierarchy fixture", Level: level, ParentCategory: parent, IsTab: true})
		if err != nil {
			t.Fatal(err)
		}
		return r.Id
	}
	root, other := create(1, 0), create(1, 0)
	child := create(2, root)
	leaf := create(3, child)
	// Moving a subtree within its level preserves every descendant's level.
	for i := 0; i < 2; i++ {
		if _, err := client.UpdateCategory(ctx, &proto.CategoryInfoRequest{Id: child, ParentCategory: other, Level: 2}); err != nil {
			t.Fatal(err)
		}
	}
	// A leaf may change level when the resulting parent matches that level.
	if _, err := client.UpdateCategory(ctx, &proto.CategoryInfoRequest{Id: leaf, ParentCategory: root, Level: 2}); err != nil {
		t.Fatal(err)
	}
	// Soft-deleted children do not constrain a parent's level.
	deleted := create(3, child)
	if _, err := client.DeleteCategory(ctx, &proto.DeleteCategoryRequest{Id: deleted}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateCategory(ctx, &proto.CategoryInfoRequest{Id: child, ParentCategory: leaf, Level: 3}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct{ id, level, parent int32 }{{root, 1, 0}, {other, 1, 0}, {leaf, 2, root}, {child, 3, leaf}} {
		got, err := client.GetSubCategory(ctx, &proto.CategoryListRequest{Id: want.id})
		if err != nil || got.GetInfo().GetLevel() != want.level || got.GetInfo().GetParentCategory() != want.parent {
			t.Fatalf("hierarchy mismatch: %v, %v", got, err)
		}
	}
}
