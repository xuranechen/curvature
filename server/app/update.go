package app

import (
	"context"
	"io"
)

type UpdateOptions struct {
	Version  string
	Args     []string
	Progress io.Writer
}

type UpdateResult struct {
	CurrentVersion string
	LatestVersion  string
	HasUpdate      bool
	Installed      bool
	Message        string
}

func UpdateNow(ctx context.Context, opts UpdateOptions) (UpdateResult, error) {
	return UpdateResult{
		CurrentVersion: opts.Version,
		Message:        "updates disabled (self-hosted build, no official download server)",
	}, nil
}
