package biotime

import (
	"context"
	"iter"
	"net/http"
	"net/url"
)

const positionsPath = "/personnel/api/positions/"

// Position is a job position.
type Position struct {
	ID           int    `json:"id"`
	PositionCode string `json:"position_code"`
	PositionName string `json:"position_name"`
	// ParentPosition is null for top-level positions.
	ParentPosition     Ref[Position] `json:"parent_position"`
	ParentPositionName string        `json:"parent_position_name,omitempty"`
}

// PositionFilter selects positions in [PositionService.List].
type PositionFilter struct {
	ListOptions
	PositionCode string
	PositionName string
	// ParentPosition filters by parent position identifier.
	ParentPosition int
	// Params holds additional raw query parameters.
	Params map[string]string
}

func (f *PositionFilter) values(pageSizeParam string) url.Values {
	q := newQuery()
	if f == nil {
		return q.Values
	}
	f.ListOptions.apply(q.Values, pageSizeParam)
	q.str("position_code", f.PositionCode)
	q.str("position_name", f.PositionName)
	q.int("parent_position", f.ParentPosition)
	for k, v := range f.Params {
		q.Set(k, v)
	}
	return q.Values
}

// PositionParams is the payload for creating or updating a position.
type PositionParams struct {
	PositionCode *string `json:"position_code,omitempty"`
	PositionName *string `json:"position_name,omitempty"`
	// ParentPosition is the parent position identifier.
	ParentPosition *int `json:"parent_position,omitempty"`
}

// PositionService accesses /personnel/api/positions/.
type PositionService struct {
	c *Client
}

// List returns one page of positions matching filter (nil for all).
func (s *PositionService) List(ctx context.Context, filter *PositionFilter) (*Page[Position], error) {
	return listPage[Position](ctx, s.c, positionsPath, filter.values(s.c.pageSizeParam))
}

// All iterates over every position matching filter.
func (s *PositionService) All(ctx context.Context, filter *PositionFilter) iter.Seq2[Position, error] {
	return iterate[Position](ctx, s.c, positionsPath, filter.values(s.c.pageSizeParam))
}

// Get returns the position with the given identifier.
func (s *PositionService) Get(ctx context.Context, id int) (*Position, error) {
	var p Position
	if err := s.c.Get(ctx, detailPath(positionsPath, id), nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Create adds a position. PositionCode and PositionName are required by the
// server.
func (s *PositionService) Create(ctx context.Context, params *PositionParams) (*Position, error) {
	var p Position
	if err := s.c.Post(ctx, positionsPath, params, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Update changes the provided fields of a position (HTTP PATCH).
func (s *PositionService) Update(ctx context.Context, id int, params *PositionParams) (*Position, error) {
	var p Position
	if err := s.c.Do(ctx, http.MethodPatch, detailPath(positionsPath, id), nil, params, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Delete removes a position.
func (s *PositionService) Delete(ctx context.Context, id int) error {
	return s.c.Do(ctx, http.MethodDelete, detailPath(positionsPath, id), nil, nil, nil)
}
