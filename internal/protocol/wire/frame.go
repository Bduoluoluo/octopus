package wire

import (
	"context"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

type Frame = model.StreamFrame

type Decoder interface {
	Push(context.Context, Frame) ([]model.StreamEvent, error)
	End(context.Context) ([]model.StreamEvent, error)
	Close() error
}

type Encoder interface {
	Push(context.Context, []model.StreamEvent) ([]Frame, error)
	End(context.Context) ([]Frame, error)
	Close() error
}
