package biotime

import (
	"context"
	"iter"
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
	ParentPositionName string        `json:"parent_position_name,omitzero"`
}

func (p *Position) recordID() int { return p.ID }

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

func (f *PositionFilter) values(cfg queryConfig) url.Values {
	if f == nil {
		return url.Values{}
	}
	return buildQuery(f.ListOptions, f.Params, cfg, func(q query) {
		q.str("position_code", f.PositionCode)
		q.str("position_name", f.PositionName)
		q.int("parent_position", f.ParentPosition)
	})
}

// PositionParams is the payload for creating or updating a position.
// PositionCode and PositionName are required by the server on create.
type PositionParams struct {
	PositionCode *string `json:"position_code,omitzero"`
	PositionName *string `json:"position_name,omitzero"`
	// ParentPosition is the parent position identifier.
	ParentPosition *int `json:"parent_position,omitzero"`
}

// PositionService accesses /personnel/api/positions/.
type PositionService struct {
	resource[Position, PositionParams, *PositionFilter]
}

// List returns one page of positions matching filter (nil for all).
func (s *PositionService) List(ctx context.Context, filter *PositionFilter) (*Page[Position], error) {
	return s.resource.List(ctx, filter)
}

// All iterates over every position matching filter, fetching pages on demand
// by following the server's "next" links. See [ListOptions] for what makes
// a walk stable.
func (s *PositionService) All(ctx context.Context, filter *PositionFilter) iter.Seq2[Position, error] {
	return s.resource.All(ctx, filter)
}

// Get returns the position with the given identifier.
func (s *PositionService) Get(ctx context.Context, id int) (*Position, error) {
	return s.resource.Get(ctx, id)
}
