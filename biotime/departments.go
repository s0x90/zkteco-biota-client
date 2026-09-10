package biotime

import (
	"context"
	"iter"
	"net/http"
	"net/url"
)

const departmentsPath = "/personnel/api/departments/"

// Department is a node of the organisation tree.
type Department struct {
	ID       int    `json:"id"`
	DeptCode string `json:"dept_code"`
	DeptName string `json:"dept_name"`
	// ParentDept is null for top-level departments.
	ParentDept     Ref[Department] `json:"parent_dept"`
	ParentDeptName string          `json:"parent_dept_name,omitempty"`
}

// DepartmentFilter selects departments in [DepartmentService.List].
type DepartmentFilter struct {
	ListOptions
	DeptCode string
	DeptName string
	// ParentDept filters by parent department identifier.
	ParentDept int
	// Params holds additional raw query parameters.
	Params map[string]string
}

func (f *DepartmentFilter) values(pageSizeParam string) url.Values {
	q := newQuery()
	if f == nil {
		return q.Values
	}
	f.ListOptions.apply(q.Values, pageSizeParam)
	q.str("dept_code", f.DeptCode)
	q.str("dept_name", f.DeptName)
	q.int("parent_dept", f.ParentDept)
	for k, v := range f.Params {
		q.Set(k, v)
	}
	return q.Values
}

// DepartmentParams is the payload for creating or updating a department.
type DepartmentParams struct {
	DeptCode *string `json:"dept_code,omitempty"`
	DeptName *string `json:"dept_name,omitempty"`
	// ParentDept is the parent department identifier.
	ParentDept *int `json:"parent_dept,omitempty"`
}

// DepartmentService accesses /personnel/api/departments/.
type DepartmentService struct {
	c *Client
}

// List returns one page of departments matching filter (nil for all).
func (s *DepartmentService) List(ctx context.Context, filter *DepartmentFilter) (*Page[Department], error) {
	return listPage[Department](ctx, s.c, departmentsPath, filter.values(s.c.pageSizeParam))
}

// All iterates over every department matching filter.
func (s *DepartmentService) All(ctx context.Context, filter *DepartmentFilter) iter.Seq2[Department, error] {
	return iterate[Department](ctx, s.c, departmentsPath, filter.values(s.c.pageSizeParam))
}

// Get returns the department with the given identifier.
func (s *DepartmentService) Get(ctx context.Context, id int) (*Department, error) {
	var d Department
	if err := s.c.Get(ctx, detailPath(departmentsPath, id), nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Create adds a department. DeptCode and DeptName are required by the server.
func (s *DepartmentService) Create(ctx context.Context, params *DepartmentParams) (*Department, error) {
	var d Department
	if err := s.c.Post(ctx, departmentsPath, params, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Update changes the provided fields of a department (HTTP PATCH).
func (s *DepartmentService) Update(ctx context.Context, id int, params *DepartmentParams) (*Department, error) {
	var d Department
	if err := s.c.Do(ctx, http.MethodPatch, detailPath(departmentsPath, id), nil, params, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Delete removes a department.
func (s *DepartmentService) Delete(ctx context.Context, id int) error {
	return s.c.Do(ctx, http.MethodDelete, detailPath(departmentsPath, id), nil, nil, nil)
}
