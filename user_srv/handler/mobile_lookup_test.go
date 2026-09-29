package handler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"grpctest/user_srv/model/proto"
)

func TestMobileLookupDistinguishesDatabaseOutcomes(t *testing.T) {
	for _, outcome := range []string{"found", "missing", "database failure", "canceled", "expired", "query deadline"} {
		t.Run(outcome, func(t *testing.T) {
			mock := profileMockDB(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := codes.OK
			rows := sqlmock.NewRows([]string{"id", "mobile", "password", "role"})
			switch outcome {
			case "canceled":
				cancel()
				want = codes.Canceled
			case "expired":
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
				want = codes.DeadlineExceeded
			default:
				query := mock.ExpectQuery("SELECT .* FROM `user`").WithArgs("13000000001", 1)
				switch outcome {
				case "missing":
					query.WillReturnRows(rows)
					want = codes.NotFound
				case "database failure":
					query.WillReturnError(errors.New("private database detail"))
					want = codes.Internal
				case "query deadline":
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, 30*time.Millisecond)
					defer stop()
					query.WillDelayFor(300 * time.Millisecond).WillReturnRows(rows)
					want = codes.DeadlineExceeded
				default:
					query.WillReturnRows(rows.AddRow(41, "13000000001", "test-hash", 1))
				}
			}
			got, err := (&UserServer{}).GetUserByMobile(ctx, &proto.MobileRequest{Mobile: "13000000001"})
			if status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("lookup code=%s want=%s", status.Code(err), want)
			}
			if want != codes.OK && got != nil {
				t.Fatal("failed lookup returned user data")
			}
			if want == codes.OK && (got.GetId() != 41 || got.GetMobile() != "13000000001" || got.GetPassword() != "test-hash" || got.GetRole() != 1) {
				t.Fatal("successful lookup lost login fields")
			}
		})
	}
}
