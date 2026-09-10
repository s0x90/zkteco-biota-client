package biotime

import (
	"context"
	"iter"
	"net/http"
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
	ParentAreaName string    `json:"parent_area_name,omitempty"`
}

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

func (f *AreaFilter) values(pageSizeParam string) url.Values {
	q := newQuery()
	if f == nil {
		return q.Values
	}
	f.ListOptions.apply(q.Values, pageSizeParam)
	q.str("area_code", f.AreaCode)
	q.str("area_name", f.AreaName)
	q.int("parent_area", f.ParentArea)
	for k, v := range f.Params {
		q.Set(k, v)
	}
	return q.Values
}

// AreaParams is the payload for creating or updating an area.
type AreaParams struct {
	AreaCode *string `json:"area_code,omitempty"`
	AreaName *string `json:"area_name,omitempty"`
	// ParentArea is the parent area identifier.
	ParentArea *int `json:"parent_area,omitempty"`
}

// AreaService accesses /personnel/api/areas/.
type AreaService struct {
	c *Client
}

// List returns one page of areas matching filter (nil for all).
func (s *AreaService) List(ctx context.Context, filter *AreaFilter) (*Page[Area], error) {
	return listPage[Area](ctx, s.c, areasPath, filter.values(s.c.pageSizeParam))
}

// All iterates over every area matching filter.
func (s *AreaService) All(ctx context.Context, filter *AreaFilter) iter.Seq2[Area, error] {
	return iterate[Area](ctx, s.c, areasPath, filter.values(s.c.pageSizeParam))
}

// Get returns the area with the given identifier.
func (s *AreaService) Get(ctx context.Context, id int) (*Area, error) {
	var a Area
	if err := s.c.Get(ctx, detailPath(areasPath, id), nil, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// Create adds an area. AreaCode and AreaName are required by the server.
func (s *AreaService) Create(ctx context.Context, params *AreaParams) (*Area, error) {
	var a Area
	if err := s.c.Post(ctx, areasPath, params, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// Update changes the provided fields of an area (HTTP PATCH).
func (s *AreaService) Update(ctx context.Context, id int, params *AreaParams) (*Area, error) {
	var a Area
	if err := s.c.Do(ctx, http.MethodPatch, detailPath(areasPath, id), nil, params, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// Delete removes an area.
func (s *AreaService) Delete(ctx context.Context, id int) error {
	return s.c.Do(ctx, http.MethodDelete, detailPath(areasPath, id), nil, nil, nil)
}
