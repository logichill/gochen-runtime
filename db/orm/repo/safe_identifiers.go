package repo

import (
	"reflect"

	"gochen-runtime/db/sql/safeident"
	"gochen/errors"
)

func validateSafeIdentifier(name string, source string) error {
	if name == "" {
		return nil
	}
	if err := safeident.ValidateQualifiedIdentifier(name, source, 1); err == nil {
		return nil
	}
	return errors.NewCode(errors.InvalidInput, "unsafe repository identifier").
		WithContext("identifier", name).
		WithContext("source", source)
}

func (r *Repo[T, ID]) validateColumnIdentifiers() error {
	if r == nil {
		return nil
	}
	schema := r.accessSchema()
	checks := []struct {
		name   string
		source string
	}{
		{name: schema.managedScope.column, source: "access.managed_scope"},
		{name: schema.isolation.column, source: "access.tenant_id"},
		{name: schema.ownerID.column, source: "access.owner_id"},
		{name: schema.version.column, source: "access.version"},
		{name: r.softDeleteCols.DeletedAt, source: "soft_delete.deleted_at"},
		{name: r.softDeleteCols.DeletedBy, source: "soft_delete.deleted_by"},
	}
	for _, check := range checks {
		if err := validateSafeIdentifier(check.name, check.source); err != nil {
			return err
		}
	}
	return nil
}

func validateSemanticColumnTags[T any]() error {
	var zero T
	typ := reflect.TypeOf(zero)
	for typ != nil && typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	return validateSemanticColumnTagsRecursive(typ, map[reflect.Type]int{})
}

func validateSemanticColumnTagsRecursive(typ reflect.Type, visiting map[reflect.Type]int) error {
	if typ == nil {
		return nil
	}
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || isTimeType(typ) {
		return nil
	}
	if visiting[typ] > 0 {
		return nil
	}
	visiting[typ]++
	defer func() {
		visiting[typ]--
	}()

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}
		explicitColumn := columnFromTags(field)
		if explicitColumn != "" {
			switch field.Name {
			case "ManagedScopeID", "TenantID", "OwnerID", "Version":
				if err := validateSafeIdentifier(explicitColumn, "tag."+field.Name); err != nil {
					return err
				}
			}
		}
		fieldType := field.Type
		for fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}
		if fieldType.Kind() == reflect.Struct && !isTimeType(fieldType) {
			if err := validateSemanticColumnTagsRecursive(fieldType, visiting); err != nil {
				return err
			}
		}
	}
	return nil
}
