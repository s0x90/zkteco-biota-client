package biotime

import (
	"context"
	"iter"
	"net/url"
)

const areasPath = "/personnel/api/areas/"

// Area groups devices; employees assigned to an area are pushed to its
// devices.
type Area struct {
	ID       int    `json:"id"`
	AreaCode string `json:"area_code"`
	AreaName string `json:"area_name"`
	// ParentArea is null for top-level areas.
	ParentArea     Ref[Area] `json:"parent_area"`
	ParentAreaName string    `json:"parent_area_name,omitzero"`
}

func (a *Area) recordID() int { return a.ID }

// AreaFilter selects areas in [AreaService.List].
type AreaFilter struct {
	ListOptions
	AreaCode string
	AreaName string
	// ParentArea filters by parent area identifier.
	ParentArea int
	// Params holds additional raw query parameters.
	Params map[string]string
}

func (f *AreaFilter) values(cfg queryConfig) url.Values {
	if f == nil {
		return url.Values{}
	}
	return buildQuery(f.ListOptions, f.Params, cfg, func(q query) {
		q.str("area_code", f.AreaCode)
		q.str("area_name", f.AreaName)
		q.int("parent_area", f.ParentArea)
	})
}

// AreaParams is the payload for creating or updating an area. AreaCode and
// AreaName are required by the server on create.
type AreaParams struct {
	AreaCode *string `json:"area_code,omitzero"`
	AreaName *string `json:"area_name,omitzero"`
	// ParentArea is the parent area identifier.
	ParentArea *int `json:"parent_area,omitzero"`
}

// AreaService accesses /personnel/api/areas/.
type AreaService struct {
	resource[Area, AreaParams, *AreaFilter]
}

// List returns one page of areas matching filter (nil for all).
func (s *AreaService) List(ctx context.Context, filter *AreaFilter) (*Page[Area], error) {
	return s.resource.List(ctx, filter)
}

// All iterates over every area matching filter, fetching pages on demand
// by following the server's "next" links. See [ListOptions] for what makes
// a walk stable.
func (s *AreaService) All(ctx context.Context, filter *AreaFilter) iter.Seq2[Area, error] {
	return s.resource.All(ctx, filter)
}

// Get returns the area with the given identifier.
func (s *AreaService) Get(ctx context.Context, id int) (*Area, error) {
	return s.resource.Get(ctx, id)
}
