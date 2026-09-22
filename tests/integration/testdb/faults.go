//go:build integration

package testdb

import (
	"context"
	"database/sql"
	"errors"
	"gorm.io/gorm"
	"testing"
)

// Wrap the real MySQL transaction to lose a commit reply, or fail before committing.
type commitFaultPool struct {
	gorm.ConnPool
	begins int
	target int
	after  bool
}

func (p *commitFaultPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	p.begins++
	tx, err := p.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &commitFaultTx{Tx: tx, fail: p.begins == p.target, after: p.after}, nil
}

type commitFaultTx struct {
	*sql.Tx
	fail, after bool
}

func (tx *commitFaultTx) Commit() error {
	if !tx.fail {
		return tx.Tx.Commit()
	}
	if tx.after {
		if err := tx.Tx.Commit(); err != nil {
			return err
		}
	} else {
		if err := tx.Tx.Rollback(); err != nil {
			return err
		}
	}
	return errors.New("injected commit acknowledgment failure")
}
func FailCommit(t *testing.T, db *gorm.DB, target int, after bool) {
	t.Helper()
	old := db.Statement.ConnPool
	pool := &commitFaultPool{ConnPool: old, target: target, after: after}
	db.Statement.ConnPool = pool
	t.Cleanup(func() { db.Statement.ConnPool = old })
}
