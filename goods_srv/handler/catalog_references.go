package handler

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// Call only after locking the category/brand FOR UPDATE in a new transaction.
// Reference writers hold a shared lock on that same row until they commit.
// The first consistent read is therefore taken after earlier writers finish;
// plain counts avoid locking child rows in the reverse order of their updates.
func rejectCatalogReferences(query *gorm.DB, message string) error {
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return status.Error(codes.FailedPrecondition, message)
	}
	return nil
}
