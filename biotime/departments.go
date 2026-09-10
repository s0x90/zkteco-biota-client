package biotime

import (
	"context"
	"iter"
	"net/url"
)

const departmentsPath = "/personnel/api/departments/"

// Department is a node of the organization tree.
type Department struct {
	ID       int    `json:"id"`
	DeptCode string `json:"dept_code"`
	DeptName string `json:"dept_name"`
	// ParentDept is null for top-level departments.
	ParentDept     Ref[Department] `json:"parent_dept"`
	ParentDeptName string          `json:"parent_dept_name,omitzero"`
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
	if f == nil {
		return url.Values{}
	}
	return buildQuery(f.ListOptions, f.Params, pageSizeParam, func(q query) {
		q.str("dept_code", f.DeptCode)
		q.str("dept_name", f.DeptName)
		q.int("parent_dept", f.ParentDept)
	})
}

// DepartmentParams is the payload for creating or updating a department.
// DeptCode and DeptName are required by the server on create.
type DepartmentParams struct {
	DeptCode *string `json:"dept_code,omitzero"`
	DeptName *string `json:"dept_name,omitzero"`
	// ParentDept is the parent department identifier.
	ParentDept *int `json:"parent_dept,omitzero"`
}

// DepartmentService accesses /personnel/api/departments/.
type DepartmentService struct {
	resource[Department, DepartmentParams, *DepartmentFilter]
}

// List returns one page of departments matching filter (nil for all).
func (s *DepartmentService) List(ctx context.Context, filter *DepartmentFilter) (*Page[Department], error) {
	return s.resource.List(ctx, filter)
}

// All iterates over every department matching filter, fetching pages on demand
// by following the server's "next" links. See [ListOptions] for what makes
// a walk stable.
func (s *DepartmentService) All(ctx context.Context, filter *DepartmentFilter) iter.Seq2[Department, error] {
	return s.resource.All(ctx, filter)
}

// Get returns the department with the given identifier.
func (s *DepartmentService) Get(ctx context.Context, id int) (*Department, error) {
	return s.resource.Get(ctx, id)
}
