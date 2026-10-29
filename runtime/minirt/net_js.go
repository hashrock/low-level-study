//go:build js

package minirt

import (
	"context"
	"errors"
)

type noNetwork struct{}

func (noNetwork) Fetch(context.Context, FetchRequest, func(string) error) (*FetchResponse, error) {
	return nil, errors.New("fetch: no network in this build (use FakeNetwork)")
}

func defaultNetwork() Network { return noNetwork{} }
