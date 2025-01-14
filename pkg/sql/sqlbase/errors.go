// Copyright 2015 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

package sqlbase

import (
	"fmt"
	"strings"

	"golang.org/x/net/context"

	"github.com/cockroachdb/cockroach/pkg/internal/client"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/sql/parser"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgerror"
)

// Cockroach error extensions:
const (
	// CodeRangeUnavailable signals that some data from the cluster cannot be
	// accessed (e.g. because all replicas awol).
	// We're using the postgres "Internal Error" error class "XX".
	CodeRangeUnavailable string = "XXC00"
)

const (
	txnAbortedMsg = "current transaction is aborted, commands ignored " +
		"until end of transaction block"
	txnCommittedMsg = "current transaction is committed, commands ignored " +
		"until end of transaction block"
	txnRetryMsgPrefix = "restart transaction"
)

// NewRetryError creates an error signifying that the transaction can be retried.
// It signals to the user that the SQL txn entered the RESTART_WAIT state after a
// serialization error, and that a ROLLBACK TO SAVEPOINT COCKROACH_RESTART statement
// should be issued.
func NewRetryError(cause error) error {
	return pgerror.NewErrorf(
		pgerror.CodeSerializationFailureError, "%s: %s", txnRetryMsgPrefix, cause)
}

// NewTransactionAbortedError creates an error for trying to run a command in
// the context of transaction that's already aborted.
func NewTransactionAbortedError(customMsg string) error {
	if customMsg != "" {
		return pgerror.NewErrorf(
			pgerror.CodeInFailedSQLTransactionError, "%s: %s", customMsg, txnAbortedMsg)
	}
	return pgerror.NewError(pgerror.CodeInFailedSQLTransactionError, txnAbortedMsg)
}

// NewTransactionCommittedError creates an error that signals that the SQL txn
// is in the COMMIT_WAIT state and that only a COMMIT statement will be accepted.
func NewTransactionCommittedError() error {
	return pgerror.NewError(pgerror.CodeInvalidTransactionStateError, txnCommittedMsg)
}

// NewNonNullViolationError creates an error for a violation of a non-NULL constraint.
func NewNonNullViolationError(columnName string) error {
	return pgerror.NewErrorf(pgerror.CodeNotNullViolationError, "null value in column %q violates not-null constraint", columnName)
}

// NewUniquenessConstraintViolationError creates an error that represents a
// violation of a UNIQUE constraint.
func NewUniquenessConstraintViolationError(index *IndexDescriptor, vals []parser.Datum) error {
	valStrs := make([]string, 0, len(vals))
	for _, val := range vals {
		valStrs = append(valStrs, val.String())
	}

	return pgerror.NewErrorf(pgerror.CodeUniqueViolationError,
		"duplicate key value (%s)=(%s) violates unique constraint %q",
		strings.Join(index.ColumnNames, ","),
		strings.Join(valStrs, ","),
		index.Name)
}

// IsUniquenessConstraintViolationError returns true if the error is for a
// uniqueness constraint violation.
func IsUniquenessConstraintViolationError(err error) bool {
	return errHasCode(err, pgerror.CodeUniqueViolationError)
}

// NewInvalidSchemaDefinitionError creates an error for an invalid schema
// definition such as a schema definition that doesn't parse.
func NewInvalidSchemaDefinitionError(err error) error {
	return pgerror.NewError(pgerror.CodeInvalidSchemaDefinitionError, err.Error())
}

// IsPermanentSchemaChangeError returns true if the error results in
// a permanent failure of a schema change.
func IsPermanentSchemaChangeError(err error) bool {
	return errHasCode(err, pgerror.CodeNotNullViolationError) ||
		errHasCode(err, pgerror.CodeUniqueViolationError) ||
		errHasCode(err, pgerror.CodeInvalidSchemaDefinitionError)
}

// NewUndefinedDatabaseError creates an error that represents a missing database.
func NewUndefinedDatabaseError(name string) error {
	// Postgres will return an UndefinedTable error on queries that go to a "relation"
	// that does not exist (a query to a non-existent table or database), but will
	// return an InvalidCatalogName error when connecting to a database that does
	// not exist. We've chosen to return this code for all cases where the error cause
	// is a missing database.
	return pgerror.NewErrorf(
		pgerror.CodeInvalidCatalogNameError, "database %q does not exist", name)
}

// IsUndefinedDatabaseError returns true if the error is for an undefined database.
func IsUndefinedDatabaseError(err error) bool {
	return errHasCode(err, pgerror.CodeInvalidCatalogNameError)
}

// NewUndefinedRelationError creates an error that represents a missing database table or view.
func NewUndefinedRelationError(name parser.NodeFormatter) error {
	return pgerror.NewErrorf(pgerror.CodeUndefinedTableError,
		"relation %q does not exist", parser.ErrString(name))
}

// IsUndefinedRelationError returns true if the error is for an undefined table.
func IsUndefinedRelationError(err error) bool {
	return errHasCode(err, pgerror.CodeUndefinedTableError)
}

// NewDatabaseAlreadyExistsError creates an error for a preexisting database.
func NewDatabaseAlreadyExistsError(name string) error {
	return pgerror.NewErrorf(pgerror.CodeDuplicateDatabaseError, "database %q already exists", name)
}

// NewRelationAlreadyExistsError creates an error for a preexisting relation.
func NewRelationAlreadyExistsError(name string) error {
	return pgerror.NewErrorf(pgerror.CodeDuplicateRelationError, "relation %q already exists", name)
}

// NewWrongObjectTypeError creates a wrong object type error.
func NewWrongObjectTypeError(name *parser.TableName, desiredObjType string) error {
	return pgerror.NewErrorf(pgerror.CodeWrongObjectTypeError, "%q is not a %s",
		parser.ErrString(name), desiredObjType)
}

// NewSyntaxError creates a syntax error.
func NewSyntaxError(msg string) error {
	return pgerror.NewError(pgerror.CodeSyntaxError, msg)
}

// NewDependentObjectError creates a dependent object error.
func NewDependentObjectError(msg string) error {
	return pgerror.NewError(pgerror.CodeDependentObjectsStillExistError, msg)
}

// NewDependentObjectErrorWithHint creates a dependent object error with a hint
func NewDependentObjectErrorWithHint(msg string, hint string) error {
	pErr := pgerror.NewError(pgerror.CodeDependentObjectsStillExistError, msg)
	pErr.Hint = hint
	return pErr
}

// NewRangeUnavailableError creates an unavailable range error.
func NewRangeUnavailableError(
	rangeID roachpb.RangeID, origErr error, nodeIDs ...roachpb.NodeID,
) error {
	return pgerror.NewErrorf(CodeRangeUnavailable, "key range id:%d is unavailable; missing nodes: %s. Original error: %v",
		rangeID, nodeIDs, origErr)
}

// NewWindowingError creates a windowing error.
func NewWindowingError(in string) error {
	return pgerror.NewErrorf(pgerror.CodeWindowingError, "window functions are not allowed in %s", in)
}

// NewStatementCompletionUnknownError creates an error with the corresponding pg
// code. This is used to inform the client that it's unknown whether a statement
// succeeded or not. Of particular interest to clients is when this error is
// returned for a statement outside of a transaction or for a COMMIT/RELEASE
// SAVEPOINT - there manual inspection may be necessary to check whether the
// statement/transaction committed. When this is returned for other
// transactional statements, the transaction has been rolled back (like it is
// for any errors).
//
// NOTE(andrei): When introducing this error, I haven't verified the exact
// conditions under which Postgres returns this code, nor its relationship to
// code CodeTransactionResolutionUnknownError. I couldn't find good
// documentation on these codes.
func NewStatementCompletionUnknownError(err *roachpb.AmbiguousResultError) error {
	return pgerror.NewErrorf(pgerror.CodeStatementCompletionUnknownError, err.Error())
}

// NewQueryCanceledError creates a query cancellation error.
func NewQueryCanceledError() error {
	return pgerror.NewErrorf(pgerror.CodeQueryCanceledError, "query execution canceled")
}

// IsQueryCanceledError checks whether this is a query canceled error.
func IsQueryCanceledError(err error) bool {
	return errHasCode(err, pgerror.CodeQueryCanceledError) || strings.Contains(err.Error(), "query execution canceled")
}

func errHasCode(err error, code string) bool {
	if pgErr, ok := pgerror.GetPGCause(err); ok {
		return pgErr.Code == code
	}
	return false
}

type singleKVFetcher struct {
	kv   client.KeyValue
	done bool
}

func (f *singleKVFetcher) nextKV(ctx context.Context) (bool, client.KeyValue, error) {
	if f.done {
		return false, client.KeyValue{}, nil
	}
	f.done = true
	return true, f.kv, nil
}

func (f *singleKVFetcher) getRangesInfo() []roachpb.RangeInfo {
	panic("getRangesInfo() called on singleKVFetcher")
}

// ConvertBatchError returns a user friendly constraint violation error.
func ConvertBatchError(ctx context.Context, tableDesc *TableDescriptor, b *client.Batch) error {
	origPErr := b.MustPErr()
	if origPErr.Index == nil {
		return origPErr.GoError()
	}
	j := origPErr.Index.Index
	if j >= int32(len(b.Results)) {
		panic(fmt.Sprintf("index %d outside of results: %+v", j, b.Results))
	}
	result := b.Results[j]
	var alloc DatumAlloc
	if cErr, ok := origPErr.GetDetail().(*roachpb.ConditionFailedError); ok && len(result.Rows) > 0 {
		key := result.Rows[0].Key
		// TODO(dan): There's too much internal knowledge of the sql table
		// encoding here (and this callsite is the only reason
		// DecodeIndexKeyPrefix is exported). Refactor this bit out.
		indexID, _, err := DecodeIndexKeyPrefix(&alloc, tableDesc, key)
		if err != nil {
			return err
		}
		index, err := tableDesc.FindIndexByID(indexID)
		if err != nil {
			return err
		}
		var rf RowFetcher
		colIdxMap := make(map[ColumnID]int, len(index.ColumnIDs))
		cols := make([]ColumnDescriptor, len(index.ColumnIDs))
		valNeededForCol := make([]bool, len(index.ColumnIDs))
		for i, colID := range index.ColumnIDs {
			colIdxMap[colID] = i
			col, err := tableDesc.FindColumnByID(colID)
			if err != nil {
				return err
			}
			cols[i] = *col
			valNeededForCol[i] = true
		}
		if err := rf.Init(tableDesc, colIdxMap, index, false, /* reverse */
			indexID != tableDesc.PrimaryIndex.ID /* isSecondaryIndex */, cols, valNeededForCol,
			false /* returnRangeInfo */, &DatumAlloc{}); err != nil {
			return err
		}
		f := singleKVFetcher{kv: client.KeyValue{Key: key, Value: cErr.ActualValue}}
		// Use the RowFetcher to decode the single kv pair above by passing in
		// this singleKVFetcher implementation, which doesn't actually hit KV.
		if err := rf.StartScanFrom(ctx, &f); err != nil {
			return err
		}
		decodedVals, err := rf.NextRowDecoded(ctx, false /* traceKV */)
		if err != nil {
			return err
		}
		return NewUniquenessConstraintViolationError(index, decodedVals)
	}
	return origPErr.GoError()
}
