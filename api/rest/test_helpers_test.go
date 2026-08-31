package rest

import (
	"context"
	"time"

	"gochen/domain/crud"
)

type fakeEntity struct {
	ID        int64     `json:"id" query:"nofilter,select"`
	Name      string    `json:"name,omitempty"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	Version   uint64    `json:"version" query:"-"`
}

func (f *fakeEntity) GetID() int64       { return f.ID }
func (f *fakeEntity) GetVersion() uint64 { return f.Version }

type noopRepository struct{}

func (noopRepository) Create(context.Context, *fakeEntity) error       { return nil }
func (noopRepository) Get(context.Context, int64) (*fakeEntity, error) { return nil, nil }
func (noopRepository) Update(context.Context, *fakeEntity) error       { return nil }
func (noopRepository) Delete(context.Context, int64) error             { return nil }
func (noopRepository) List(context.Context, int, int) ([]*fakeEntity, error) {
	return []*fakeEntity{}, nil
}
func (noopRepository) Count(context.Context) (int64, error)        { return 0, nil }
func (noopRepository) Exists(context.Context, int64) (bool, error) { return true, nil }

type trackingValidator struct {
	err    error
	called int
}

func (t *trackingValidator) Validate(any) error {
	t.called++
	return t.err
}

type capturingRepository struct {
	gotUpdate   *fakeEntity
	updateCalls int
	getCalls    int
}

var _ crud.IRepository[*fakeEntity, int64] = (*capturingRepository)(nil)

func (r *capturingRepository) Create(context.Context, *fakeEntity) error { return nil }
func (r *capturingRepository) Update(_ context.Context, e *fakeEntity) error {
	r.updateCalls++
	r.gotUpdate = e
	return nil
}
func (r *capturingRepository) Delete(context.Context, int64) error { return nil }
func (r *capturingRepository) Get(_ context.Context, id int64) (*fakeEntity, error) {
	r.getCalls++
	return &fakeEntity{ID: id, Name: "existing"}, nil
}
func (r *capturingRepository) List(context.Context, int, int) ([]*fakeEntity, error) {
	return []*fakeEntity{}, nil
}
func (r *capturingRepository) Count(context.Context) (int64, error)        { return 0, nil }
func (r *capturingRepository) Exists(context.Context, int64) (bool, error) { return true, nil }
