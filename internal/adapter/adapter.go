package adapter

import (
	"context"
	"errors"
	"memeval/internal/protocol"
	"time"
)

var ErrUnsupported = errors.New("unsupported capability")

// UnsupportedError carries only a sanitized diagnostic from the adapter boundary.
type UnsupportedError struct {
	Note  string
	Cause error
}

func (e *UnsupportedError) Error() string           { return e.Note }
func (e *UnsupportedError) Unwrap() error           { return e.Cause }
func (e *UnsupportedError) Is(target error) bool    { return target == ErrUnsupported }
func (e *UnsupportedError) UnsupportedNote() string { return e.Note }

type ContractError struct{ Reason string }

func (e *ContractError) Error() string { return e.Reason }

type IngestError struct{}

func (*IngestError) Error() string { return "ingestion or visibility barrier failed" }

// Diagnostics are cumulative for the current Reset, including failed episodes.
type DiagnosticSource interface{ Diagnostics() map[string]int }

type Adapter interface {
	Describe(context.Context) (protocol.Describe, error)
	Reset(context.Context, string) error
	Ingest(context.Context, string, []protocol.PublicEvent) (protocol.IngestHandle, error)
	Ready(context.Context, string, protocol.IngestHandle, time.Time) (protocol.ReadyResult, error)
	Retrieve(context.Context, string, protocol.RetrieveRequest) (protocol.RetrieveResponse, error)
	Answer(context.Context, string, protocol.RetrieveRequest) (protocol.AnswerResponse, error)
	Close(context.Context) error
}

// Dumper is optional; callers must also require describe capability "dump".
type Dumper interface {
	Dump(context.Context, string) (protocol.DumpResponse, error)
}
