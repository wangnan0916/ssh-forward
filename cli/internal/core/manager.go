package core

import (
	"context"
	"errors"
)

type Revision uint64

type Scope struct{}

func AllHosts() Scope {
	return Scope{}
}

type Snapshot struct {
	Revision Revision
}

type Command interface {
	isCommand()
}

type Outcome struct{}

type WatchOptions struct{}

type Manager interface {
	Execute(context.Context, Command) (Outcome, error)
	Snapshot(context.Context, Scope) (Snapshot, error)
	Watch(context.Context, WatchOptions) (SnapshotStream, error)
	Close(context.Context) error
}

type SnapshotStream interface {
	Next(context.Context) (Snapshot, error)
	Close() error
}

type manager struct{}

func NewManager() Manager {
	return &manager{}
}

func (*manager) Execute(context.Context, Command) (Outcome, error) {
	return Outcome{}, errors.New("execute is not implemented")
}

func (*manager) Snapshot(context.Context, Scope) (Snapshot, error) {
	return Snapshot{Revision: 0}, nil
}

func (*manager) Watch(context.Context, WatchOptions) (SnapshotStream, error) {
	return nil, errors.New("watch is not implemented")
}

func (*manager) Close(context.Context) error {
	return nil
}
