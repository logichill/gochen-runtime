package rest

import (
	"testing"

	"gochen/errors"
	"gochen/testkit"
)

func TestResourceContributesSameApplicationAndStandardRoutes(t *testing.T) {
	application := &stubApplication{descriptor: crudDescriptor()}
	binding := Resource[*mountEntity, int64](application, WithBasePath[*mountEntity, int64]("/users"))
	contribution, err := binding.ModuleBinding()
	if err != nil || len(contribution.Applications) != 1 || contribution.Applications[0] != application || len(contribution.RouteRegistrars) != 1 || contribution.RouteRegistrars[0] != binding {
		t.Fatalf("resource contribution: %+v %v", contribution, err)
	}
	router := testkit.NewRecordingRouter()
	if err := contribution.RouteRegistrars[0].RegisterRoutes(router); err != nil {
		t.Fatal(err)
	}
	expected := testkit.NewRecordingRouter()
	if err := Mount[*mountEntity, int64](expected, application, WithBasePath[*mountEntity, int64]("/users")); err != nil {
		t.Fatal(err)
	}
	actual, standard := router.RecordedRoutes(), expected.RecordedRoutes()
	if len(actual) != len(standard) {
		t.Fatalf("resource routes=%d standard=%d", len(actual), len(standard))
	}
	for i := range actual {
		if actual[i].Method != standard[i].Method || actual[i].Path != standard[i].Path {
			t.Fatalf("resource route drift: %+v %+v", actual[i], standard[i])
		}
	}
}

func TestResourceRejectsNilApplications(t *testing.T) {
	var application *stubApplication
	var nilBinding *ResourceBinding[*mountEntity, int64]
	for _, binding := range []*ResourceBinding[*mountEntity, int64]{nilBinding, Resource[*mountEntity, int64](nil), Resource[*mountEntity, int64](application)} {
		if _, err := binding.ModuleBinding(); !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("expected invalid resource: %v", err)
		}
	}
}
